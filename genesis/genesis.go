// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package genesis

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/trace"
	"github.com/ava-labs/avalanchego/x/merkledb"

	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/codec"
	"github.com/ava-labs/hypersdk/fees"
	internalfees "github.com/ava-labs/hypersdk/internal/fees"
	"github.com/ava-labs/hypersdk/state"

	safemath "github.com/ava-labs/avalanchego/utils/math"
)

var (
	_ Genesis               = (*DefaultGenesis)(nil)
	_ GenesisAndRuleFactory = (*DefaultGenesisFactory)(nil)
)

type GenesisAndRuleFactory interface {
	Load(genesisBytes []byte, upgradeBytes []byte, networkID uint32, chainID ids.ID) (Genesis, chain.RuleFactory, error)
}

type Genesis interface {
	InitializeState(ctx context.Context, tracer trace.Tracer, mu state.Mutable, balanceHandler chain.BalanceHandler, metadataManager chain.MetadataManager) error
	GetStateBranchFactor() merkledb.BranchFactor
}

type CustomAllocation struct {
	Address codec.Address `json:"address"`
	Balance uint64        `json:"balance"`
}

type DefaultGenesis struct {
	StateBranchFactor merkledb.BranchFactor `json:"stateBranchFactor"`
	CustomAllocation  []*CustomAllocation   `json:"customAllocation"`
	Rules             *Rules                `json:"initialRules"`
}

func NewDefaultGenesis(customAllocations []*CustomAllocation) *DefaultGenesis {
	return &DefaultGenesis{
		StateBranchFactor: merkledb.BranchFactor16,
		CustomAllocation:  customAllocations,
		Rules:             NewDefaultRules(),
	}
}

func (g *DefaultGenesis) InitializeState(
	ctx context.Context,
	tracer trace.Tracer,
	mu state.Mutable,
	balanceHandler chain.BalanceHandler,
	metadataManager chain.MetadataManager,
) error {
	_, span := tracer.Start(ctx, "Genesis.InitializeState")
	defer span.End()

	var (
		supply uint64
		err    error
	)
	for _, alloc := range g.CustomAllocation {
		supply, err = safemath.Add(supply, alloc.Balance)
		if err != nil {
			return err
		}
		if err := balanceHandler.AddBalance(ctx, alloc.Address, mu, alloc.Balance); err != nil {
			return fmt.Errorf("%w: addr=%s, bal=%d", err, alloc.Address, alloc.Balance)
		}
	}

	// Update chain metadata
	if err := mu.Insert(ctx, chain.HeightKey(metadataManager.HeightPrefix()), binary.BigEndian.AppendUint64(nil, 0)); err != nil {
		return fmt.Errorf("failed to set genesis height: %w", err)
	}
	if err := mu.Insert(ctx, chain.TimestampKey(metadataManager.TimestampPrefix()), binary.BigEndian.AppendUint64(nil, 0)); err != nil {
		return fmt.Errorf("failed to set genesis timestamp: %w", err)
	}
	feeManager := internalfees.NewManager(nil)
	minUnitPrice := g.Rules.GetMinUnitPrice()
	for i := fees.Dimension(0); i < fees.FeeDimensions; i++ {
		feeManager.SetUnitPrice(i, minUnitPrice[i])
	}
	if err := mu.Insert(ctx, chain.FeeKey(metadataManager.FeePrefix()), feeManager.Bytes()); err != nil {
		return fmt.Errorf("failed to set genesis fee manager: %w", err)
	}

	return nil
}

func (g *DefaultGenesis) GetStateBranchFactor() merkledb.BranchFactor {
	return g.StateBranchFactor
}

type DefaultGenesisFactory struct{}

func (DefaultGenesisFactory) Load(genesisBytes []byte, _ []byte, networkID uint32, chainID ids.ID) (Genesis, chain.RuleFactory, error) {
	genesis := &DefaultGenesis{}
	if err := json.Unmarshal(genesisBytes, genesis); err != nil {
		return nil, nil, err
	}
	genesis.Rules.NetworkID = networkID
	genesis.Rules.ChainID = chainID

	return genesis, &ImmutableRuleFactory{genesis.Rules}, nil
}
