package vm

import (
	"context"
	"fmt"

	"github.com/ava-labs/avalanchego/api/metrics"
	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/trace"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/avalanchego/x/merkledb"
	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/chainindex"
	hcontext "github.com/ava-labs/hypersdk/context"
	"github.com/ava-labs/hypersdk/internal/pebble"
	"github.com/ava-labs/hypersdk/state"
	"github.com/ava-labs/hypersdk/state/tstate"
	"github.com/ava-labs/hypersdk/storage"
	"go.uber.org/zap"
)

// static info
// validator set (from P-Chain)
// create backend / options for all components to share (API/network/statesync)
// chain initializer (refactor out into static code)
// state syncer (merkledb + validity window)
// construct chain in two different ways that fulfills required deps

// first class citizens set by option
// gossiper
// builder
// mempool
// state sync

// Formalize options exposed to anything the VM can configure
type ComponentBackend interface {
	AddCloser(name string, closeF func() error)
	RegisterOnNormalOp(func() error)
	RegisterMempoolUpdated(func(context.Context))
}

func (vm *VM) AddCloser(name string, closeF func() error) {
	vm.snowApp.AddCloser(name, closeF)
}

func (vm *VM) RegisterOnNormalOp(onNormalOpF func() error) {
	vm.options.onNormalOp = append(vm.options.onNormalOp, onNormalOpF)
}

func (vm *VM) RegisterMempoolUpdated(onMempoolUpdatedF func(context.Context)) {
	vm.options.onMempoolUpdated = append(vm.options.onMempoolUpdated, onMempoolUpdatedF)
}

func CreateChainIndex[T chainindex.Block](
	log logging.Logger,
	sharedConfig hcontext.Config,
	componentBackend ComponentBackend,
	parentDataDir string,
	namespace string,
	multiGatherer metrics.MultiGatherer,
	parser chainindex.Parser[T],
) (*chainindex.ChainIndex[T], error) {
	blockDBRegistry, err := metrics.MakeAndRegister(multiGatherer, blockDB)
	if err != nil {
		return nil, fmt.Errorf("failed to register %s metrics: %w", blockDB, err)
	}
	pebbleConfig := pebble.NewDefaultConfig()
	chainStoreDB, err := storage.New(pebbleConfig, parentDataDir, blockDB, blockDBRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to create chain index database: %w", err)
	}
	componentBackend.AddCloser(chainIndexNamespace, chainStoreDB.Close)
	config, err := GetChainIndexConfig(sharedConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create chain index config: %w", err)
	}
	chainIndex, err := chainindex.New[T](log, blockDBRegistry, config, parser, chainStoreDB)
	if err != nil {
		return nil, fmt.Errorf("failed to create chain index: %w", err)
	}
	return chainIndex, nil
}

func extractStateHeight(ctx context.Context, im state.Immutable, metadataManager chain.MetadataManager) (uint64, error) {
	heightBytes, err := im.GetValue(ctx, chain.HeightKey(metadataManager.HeightPrefix()))
	if err != nil {
		return 0, fmt.Errorf("failed to get state height: %w", err)
	}
	stateHeight, err := database.ParseUInt64(heightBytes)
	if err != nil {
		return 0, fmt.Errorf("failed to parse state height: %w", err)
	}
	return stateHeight, nil
}

type ChainInitializer struct {
	log        logging.Logger
	tracer     trace.Tracer
	chainIndex *chainindex.ChainIndex[*chain.ExecutionBlock]
	stateDB    merkledb.MerkleDB
	chainDef   ChainDefinition
}

func NewChainInitializer(
	log logging.Logger,
	tracer trace.Tracer,
	chainIndex *chainindex.ChainIndex[*chain.ExecutionBlock],
	stateDB merkledb.MerkleDB,
	chainDef ChainDefinition,
) *ChainInitializer {
	return &ChainInitializer{
		log:        log,
		tracer:     tracer,
		chainIndex: chainIndex,
		stateDB:    stateDB,
		chainDef:   chainDef,
	}
}

func (c *ChainInitializer) initLastAccepted(ctx context.Context) (*chain.OutputBlock, error) {
	lastAcceptedHeight, err := c.chainIndex.GetLastAcceptedHeight(ctx)
	if err != nil && err != database.ErrNotFound {
		return nil, fmt.Errorf("failed to load genesis block: %w", err)
	}
	if err == database.ErrNotFound {
		return c.initGenesis(ctx)
	}
	if lastAcceptedHeight == 0 {
		blk, err := c.chainIndex.GetBlockByHeight(ctx, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch genesis block: %w", err)
		}
		return &chain.OutputBlock{
			ExecutionBlock:   blk,
			View:             c.stateDB,
			ExecutionResults: chain.ExecutionResults{},
		}, nil
	}

	// If the chain index is initialized, return the output block that matches with the latest
	// state.
	return c.initLastBlockFromState(ctx)
}

func (c *ChainInitializer) initGenesis(ctx context.Context) (*chain.OutputBlock, error) {
	ts := tstate.New(0)
	tsv := ts.NewView(state.CompletePermissions, c.stateDB, 0)
	if err := c.chainDef.genesis.InitializeState(ctx, c.tracer, tsv, c.chainDef.balanceHandler, c.chainDef.metadataManager); err != nil {
		return nil, fmt.Errorf("failed to initialize genesis state: %w", err)
	}

	// Commit genesis block post-execution state and compute root
	tsv.Commit()
	view, err := chain.CreateViewFromDiff(ctx, c.tracer, c.stateDB, ts.ChangedKeys())
	if err != nil {
		return nil, fmt.Errorf("failed to commit genesis initialized state diff: %w", err)
	}
	root, err := view.GetMerkleRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to compute genesis root: %w", err)
	}
	c.log.Info("genesis view created", zap.Stringer("root", root))
	// Create genesis block
	genesisExecutionBlk, err := chain.NewGenesisBlock(root)
	if err != nil {
		return nil, fmt.Errorf("failed to create genesis block: %w", err)
	}
	c.log.Info("committing genesis block", zap.Stringer("genesisBlock", genesisExecutionBlk))
	if err := c.chainIndex.UpdateLastAccepted(ctx, genesisExecutionBlk); err != nil {
		return nil, fmt.Errorf("failed to write genesis block: %w", err)
	}
	c.log.Info("committing genesis view")
	if err := view.CommitToDB(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit genesis view: %w", err)
	}
	c.log.Info("committed genesis")

	return &chain.OutputBlock{
		ExecutionBlock:   genesisExecutionBlk,
		View:             c.stateDB,
		ExecutionResults: chain.ExecutionResults{},
	}, nil
}

func (c *ChainInitializer) initLastBlockFromState(ctx context.Context) (*chain.OutputBlock, error) {
	stateHeight, err := extractStateHeight(ctx, c.stateDB, c.chainDef.metadataManager)
	if err != nil {
		return nil, fmt.Errorf("failed to get state hegiht for latest output block: %w", err)
	}
	blk, err := c.chainIndex.GetBlockByHeight(ctx, stateHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get block at state height %d: %w", stateHeight, err)
	}
	return &chain.OutputBlock{
		ExecutionBlock:   blk,
		View:             c.stateDB,
		ExecutionResults: chain.ExecutionResults{}, // TODO align or make it so we don't depend on this
	}, nil
}
