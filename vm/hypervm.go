// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package vm

import (
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/codec"
	"github.com/ava-labs/hypersdk/genesis"
	"github.com/ava-labs/hypersdk/snow"
	hsnow "github.com/ava-labs/hypersdk/snow"
)

var _ chain.Parser = ChainDefinition{}

type ChainDefinitionFactory struct {
	genesisFactory  genesis.GenesisAndRuleFactory
	balanceHandler  chain.BalanceHandler
	metadataManager chain.MetadataManager
	actionCodec     *codec.TypeParser[chain.Action]
	authCodec       *codec.TypeParser[chain.Auth]
	outputCodec     *codec.TypeParser[codec.Typed]
}

func (c ChainDefinitionFactory) NewChainDefinition(
	genesisBytes []byte,
	upgradeBytes []byte,
	networkID uint32,
	chainID ids.ID,
) (ChainDefinition, error) {
	genesis, ruleFactory, err := c.genesisFactory.Load(genesisBytes, upgradeBytes, networkID, chainID)
	if err != nil {
		return ChainDefinition{}, err
	}
	return ChainDefinition{
		genesis:         genesis,
		genesisBytes:    genesisBytes,
		upgradeBytes:    upgradeBytes,
		ruleFactory:     ruleFactory,
		balanceHandler:  c.balanceHandler,
		metadataManager: c.metadataManager,
		actionCodec:     c.actionCodec,
		authCodec:       c.authCodec,
		outputCodec:     c.outputCodec,
	}, nil
}

type ChainDefinition struct {
	genesis                    genesis.Genesis
	genesisBytes, upgradeBytes []byte
	ruleFactory                chain.RuleFactory
	balanceHandler             chain.BalanceHandler
	metadataManager            chain.MetadataManager
	actionCodec                *codec.TypeParser[chain.Action]
	authCodec                  *codec.TypeParser[chain.Auth]
	outputCodec                *codec.TypeParser[codec.Typed]
}

func (c ChainDefinition) Rules(t int64) chain.Rules {
	return c.ruleFactory.GetRules(t)
}

func (c ChainDefinition) ActionCodec() *codec.TypeParser[chain.Action] {
	return c.actionCodec
}

func (c ChainDefinition) OutputCodec() *codec.TypeParser[codec.Typed] {
	return c.outputCodec
}

func (c ChainDefinition) AuthCodec() *codec.TypeParser[chain.Auth] {
	return c.authCodec
}

type ComponentVM[I snow.Block, O snow.Block, A snow.Block] struct {
	chainDefFactory ChainDefinitionFactory
	chainDef        ChainDefinition

	snowInput hsnow.ChainInput
	snowApp   *hsnow.VM[I, O, A]
}


