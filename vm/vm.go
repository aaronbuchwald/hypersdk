// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package vm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"

	"github.com/ava-labs/avalanchego/api/metrics"
	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/network/p2p"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/x/merkledb"

	"github.com/ava-labs/hypersdk/api"
	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/chainindex"
	"github.com/ava-labs/hypersdk/codec"
	"github.com/ava-labs/hypersdk/event"
	"github.com/ava-labs/hypersdk/genesis"
	"github.com/ava-labs/hypersdk/internal/builder"
	"github.com/ava-labs/hypersdk/internal/gossiper"
	"github.com/ava-labs/hypersdk/internal/mempool"
	"github.com/ava-labs/hypersdk/internal/pebble"
	"github.com/ava-labs/hypersdk/internal/validators"
	"github.com/ava-labs/hypersdk/internal/validitywindow"
	"github.com/ava-labs/hypersdk/internal/workers"
	"github.com/ava-labs/hypersdk/statesync"
	"github.com/ava-labs/hypersdk/storage"

	avatrace "github.com/ava-labs/avalanchego/trace"
	hsnow "github.com/ava-labs/hypersdk/snow"
)

const (
	blockDB             = "blockdb"
	stateDB             = "statedb"
	resultsDB           = "results"
	lastResultKey       = byte(0)
	syncerDB            = "syncerdb"
	vmDataDir           = "vm"
	hyperNamespace      = "hypervm"
	chainNamespace      = "chain"
	chainIndexNamespace = "chainindex"
	gossiperNamespace   = "gossiper"

	changeProofHandlerID = 0x0
	rangeProofHandlerID  = 0x1
	txGossipHandlerID    = 0x2
)

var ErrNotAdded = errors.New("not added")

var (
	_ hsnow.Block = (*chain.ExecutionBlock)(nil)
	_ hsnow.Block = (*chain.OutputBlock)(nil)

	_ hsnow.Chain[*chain.ExecutionBlock, *chain.OutputBlock, *chain.OutputBlock] = (*VM)(nil)
	_ hsnow.ChainIndex[*chain.ExecutionBlock]                                    = (*chainindex.ChainIndex[*chain.ExecutionBlock])(nil)
)

type VM struct {
	snowInput hsnow.ChainInput
	snowApp   *hsnow.VM[*chain.ExecutionBlock, *chain.OutputBlock, *chain.OutputBlock]

	proposerMonitor *validators.ProposerMonitor

	config Config

	genesisAndRuleFactory genesis.GenesisAndRuleFactory
	genesis               genesis.Genesis
	GenesisBytes          []byte
	ruleFactory           chain.RuleFactory
	ops                   []Option
	options               *Options

	chain                   *chain.Chain
	chainTimeValidityWindow *validitywindow.TimeValidityWindow[*chain.Transaction]
	syncer                  *validitywindow.Syncer[*chain.Transaction]
	SyncClient              *statesync.Client[*chain.ExecutionBlock]

	consensusIndex *hsnow.ConsensusIndex[*chain.ExecutionBlock, *chain.OutputBlock, *chain.OutputBlock]
	chainIndex     *chainindex.ChainIndex[*chain.ExecutionBlock]

	normalOp atomic.Bool

	// Exported for integration tests
	ChainDefinition ChainDefinition
	Builder         builder.Builder
	Gossiper        gossiper.Gossiper
	Mempool         *mempool.Mempool[*chain.Transaction]

	vmAPIHandlerFactories []api.HandlerFactory[api.VM]
	rawStateDB            database.Database
	stateDB               merkledb.MerkleDB
	balanceHandler        chain.BalanceHandler
	metadataManager       chain.MetadataManager
	actionCodec           *codec.TypeParser[chain.Action]
	authCodec             *codec.TypeParser[chain.Auth]
	outputCodec           *codec.TypeParser[codec.Typed]
	authEngine            map[uint8]AuthEngine

	metrics *Metrics

	network *p2p.Network
	snowCtx *snow.Context
	DataDir string
	tracer  avatrace.Tracer
}

func New(
	genesisFactory genesis.GenesisAndRuleFactory,
	balanceHandler chain.BalanceHandler,
	metadataManager chain.MetadataManager,
	actionCodec *codec.TypeParser[chain.Action],
	authCodec *codec.TypeParser[chain.Auth],
	outputCodec *codec.TypeParser[codec.Typed],
	authEngine map[uint8]AuthEngine,
	options ...Option,
) (*VM, error) {
	allocatedNamespaces := set.NewSet[string](len(options))
	for _, option := range options {
		if allocatedNamespaces.Contains(option.Namespace) {
			return nil, fmt.Errorf("namespace %s already allocated", option.Namespace)
		}
		allocatedNamespaces.Add(option.Namespace)
	}

	return &VM{
		balanceHandler:        balanceHandler,
		metadataManager:       metadataManager,
		actionCodec:           actionCodec,
		authCodec:             authCodec,
		outputCodec:           outputCodec,
		authEngine:            authEngine,
		genesisAndRuleFactory: genesisFactory,
		ops:                   options,
	}, nil
}

// implements "block.ChainVM.common.VM"
func (vm *VM) Initialize(
	ctx context.Context,
	chainInput hsnow.ChainInput,
	snowApp *hsnow.VM[*chain.ExecutionBlock, *chain.OutputBlock, *chain.OutputBlock],
) (hsnow.ChainIndex[*chain.ExecutionBlock], *chain.OutputBlock, *chain.OutputBlock, bool, error) {
	var (
		snowCtx      = chainInput.SnowCtx
		genesisBytes = chainInput.GenesisBytes
		upgradeBytes = chainInput.UpgradeBytes
	)
	vm.DataDir = filepath.Join(snowCtx.ChainDataDir, vmDataDir)
	vm.snowCtx = snowCtx
	vm.snowInput = chainInput
	vm.snowApp = snowApp

	var err error
	vm.genesis, vm.ruleFactory, err = vm.genesisAndRuleFactory.Load(genesisBytes, upgradeBytes, vm.snowCtx.NetworkID, vm.snowCtx.ChainID)
	vm.GenesisBytes = genesisBytes
	if err != nil {
		return nil, nil, nil, false, err
	}
	vm.ChainDefinition = ChainDefinition{
		genesis:         vm.genesis,
		genesisBytes:    genesisBytes,
		upgradeBytes:    upgradeBytes,
		ruleFactory:     vm.ruleFactory,
		balanceHandler:  vm.balanceHandler,
		metadataManager: vm.metadataManager,
		actionCodec:     vm.actionCodec,
		authCodec:       vm.authCodec,
		outputCodec:     vm.outputCodec,
	}

	vmRegistry, err := metrics.MakeAndRegister(vm.snowCtx.Metrics, hyperNamespace)
	if err != nil {
		return nil, nil, nil, false, err
	}
	vm.metrics, err = newMetrics(vmRegistry)
	if err != nil {
		return nil, nil, nil, false, err
	}
	vm.proposerMonitor = validators.NewProposerMonitor(vm, vm.snowCtx)

	vm.network = snowApp.GetNetwork()

	vm.config, err = GetVMConfig(chainInput.Config)
	if err != nil {
		return nil, nil, nil, false, err
	}

	vm.tracer = chainInput.Tracer
	ctx, span := vm.tracer.Start(ctx, "VM.Initialize")
	defer span.End()

	// Instantiate DBs
	pebbleConfig := pebble.NewDefaultConfig()
	stateDBRegistry, err := metrics.MakeAndRegister(vm.snowCtx.Metrics, stateDB)
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("failed to register statedb metrics: %w", err)
	}
	vm.rawStateDB, err = storage.New(pebbleConfig, vm.snowCtx.ChainDataDir, stateDB, stateDBRegistry)
	if err != nil {
		return nil, nil, nil, false, err
	}
	vm.stateDB, err = merkledb.New(ctx, vm.rawStateDB, merkledb.Config{
		BranchFactor: vm.genesis.GetStateBranchFactor(),
		// RootGenConcurrency limits the number of goroutines
		// that will be used across all concurrent root generations
		RootGenConcurrency:          uint(vm.config.RootGenerationCores),
		HistoryLength:               uint(vm.config.StateHistoryLength),
		ValueNodeCacheSize:          uint(vm.config.ValueNodeCacheSize),
		IntermediateNodeCacheSize:   uint(vm.config.IntermediateNodeCacheSize),
		IntermediateWriteBufferSize: uint(vm.config.StateIntermediateWriteBufferSize),
		IntermediateWriteBatchSize:  uint(vm.config.StateIntermediateWriteBatchSize),
		Reg:                         stateDBRegistry,
		TraceLevel:                  merkledb.InfoTrace,
		Tracer:                      vm.tracer,
	})
	if err != nil {
		return nil, nil, nil, false, err
	}
	snowApp.AddCloser(stateDB, func() error {
		if err := vm.stateDB.Close(); err != nil {
			return fmt.Errorf("failed to close state db: %w", err)
		}
		if err := vm.rawStateDB.Close(); err != nil {
			return fmt.Errorf("failed to close raw state db: %w", err)
		}
		return nil
	})

	// Set defaults
	options := NewOptions(vm.ops...)
	err = vm.applyOptions(options)
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("failed to apply options : %w", err)
	}

	// Setup worker cluster for verifying signatures
	//
	// If [parallelism] is odd, we assign the extra
	// core to signature verification.
	authVerifiers := workers.NewParallel(vm.config.AuthVerificationCores, 100) // TODO: make job backlog a const
	snowApp.AddCloser("auth verifiers", func() error {
		authVerifiers.Stop()
		return nil
	})

	vm.chainTimeValidityWindow = validitywindow.NewTimeValidityWindow(vm.snowCtx.Log, vm.tracer, vm, func(timestamp int64) int64 {
		return vm.ruleFactory.GetRules(timestamp).GetValidityWindow()
	})
	chainRegistry, err := metrics.MakeAndRegister(vm.snowCtx.Metrics, chainNamespace)
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("failed to make %q registry: %w", chainNamespace, err)
	}
	chainConfig, err := GetChainConfig(chainInput.Config)
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("failed to get chain config: %w", err)
	}
	vm.chain, err = chain.NewChain(
		vm.tracer,
		chainRegistry,
		vm.ChainDefinition,
		vm.Mempool,
		vm.snowCtx.Log,
		vm.ruleFactory,
		vm.metadataManager,
		vm.balanceHandler,
		authVerifiers,
		vm,
		vm.chainTimeValidityWindow,
		chainConfig,
	)
	if err != nil {
		return nil, nil, nil, false, err
	}

	vm.chainIndex, err = CreateChainIndex(
		vm.snowCtx.Log,
		vm.snowInput.Config,
		vm,
		vm.DataDir,
		chainIndexNamespace,
		vm.snowCtx.Metrics,
		vm.chain,
	)
	if err != nil {
		return nil, nil, nil, false, err
	}

	if err := vm.initStateSync(ctx); err != nil {
		return nil, nil, nil, false, err
	}

	snowApp.AddNormalOpStarter(func(_ context.Context) error {
		if vm.SyncClient.Started() {
			return nil
		}
		return vm.startNormalOp(ctx)
	})

	stateReady := !vm.SyncClient.MustStateSync()
	var lastAccepted *chain.OutputBlock
	if stateReady {
		chainInitializer := NewChainInitializer(vm.snowCtx.Log, vm.tracer, vm.chainIndex, vm.stateDB, vm.ChainDefinition)
		lastAccepted, err = chainInitializer.initLastAccepted(ctx)
		if err != nil {
			return nil, nil, nil, false, err
		}
	}
	return vm.chainIndex, lastAccepted, lastAccepted, stateReady, nil
}

func (vm *VM) SetConsensusIndex(consensusIndex *hsnow.ConsensusIndex[*chain.ExecutionBlock, *chain.OutputBlock, *chain.OutputBlock]) {
	vm.consensusIndex = consensusIndex
}

func (vm *VM) startNormalOp(ctx context.Context) error {
	vm.normalOp.Store(true)

	for _, startNormalOpF := range vm.options.onNormalOp {
		if err := startNormalOpF(); err != nil {
			return err
		}
	}
	vm.checkActivity(ctx)
	return nil
}

func (vm *VM) applyOptions(o *Options) error {
	vm.Mempool = o.mempoolF(vm)

	apiBackend := NewAPIBackend(
		vm.snowInput,
		vm.ChainDefinition,
		vm.stateDB,
		vm,
		vm,
	)
	for _, apiOption := range o.apiOptions {
		opt, err := apiOption.optionFunc(apiBackend, vm.snowInput.Config.GetRawConfig(apiOption.Namespace))
		if err != nil {
			return err
		}
		opt.apply(o)
	}

	for _, apiFactory := range vm.vmAPIHandlerFactories {
		api, err := apiFactory.New(apiBackend)
		if err != nil {
			return fmt.Errorf("failed to initialize api: %w", err)
		}
		vm.snowApp.AddHandler(api.Path, api.Handler)
	}

	blockSubs := make([]event.Subscription[*chain.ExecutedBlock], len(o.blockSubscriptionFactories))
	for i, factory := range o.blockSubscriptionFactories {
		sub, err := factory.New()
		if err != nil {
			return err
		}
		blockSubs[i] = sub
	}
	executedBlockSub := event.Aggregate(blockSubs...)
	outputBlockSub := event.Map(func(b *chain.OutputBlock) *chain.ExecutedBlock {
		return &chain.ExecutedBlock{
			Block:            b.StatelessBlock,
			ExecutionResults: b.ExecutionResults,
		}
	}, executedBlockSub)
	vm.snowApp.AddAcceptedSub(outputBlockSub)
	vm.vmAPIHandlerFactories = o.vmAPIHandlerFactories

	vm.Builder = o.builderF(vm)
	gossiper, err := o.gossiperF(vm)
	if err != nil {
		return err
	}
	vm.Gossiper = gossiper
	return nil
}

func (vm *VM) checkActivity(ctx context.Context) {
	for _, onMempoolUpdateF := range vm.options.onMempoolUpdated {
		onMempoolUpdateF(ctx)
	}
}

func (vm *VM) ParseBlock(ctx context.Context, source []byte) (*chain.ExecutionBlock, error) {
	return vm.chain.ParseBlock(ctx, source)
}

func (vm *VM) BuildBlock(ctx context.Context, parent *chain.OutputBlock) (*chain.ExecutionBlock, *chain.OutputBlock, error) {
	defer vm.checkActivity(ctx)

	return vm.chain.BuildBlock(ctx, parent)
}

func (vm *VM) VerifyBlock(ctx context.Context, parent *chain.OutputBlock, block *chain.ExecutionBlock) (*chain.OutputBlock, error) {
	return vm.chain.Execute(ctx, parent.View, block, vm.normalOp.Load())
}

func (vm *VM) AcceptBlock(ctx context.Context, _ *chain.OutputBlock, block *chain.OutputBlock) (*chain.OutputBlock, error) {
	if err := vm.chain.AcceptBlock(ctx, block); err != nil {
		return nil, fmt.Errorf("failed to accept block %s: %w", block, err)
	}
	return block, nil
}
