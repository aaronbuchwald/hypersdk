// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package chaintest

import (
	"context"
	"encoding/binary"
	"math"

	"github.com/ava-labs/avalanchego/database/memdb"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/x/merkledb"
	"github.com/stretchr/testify/require"

	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/fees"
	"github.com/ava-labs/hypersdk/genesis"
	"github.com/ava-labs/hypersdk/state"
	"github.com/ava-labs/hypersdk/state/balance"
	"github.com/ava-labs/hypersdk/state/tstate"

	internalfees "github.com/ava-labs/hypersdk/internal/fees"
)

func GenerateEmptyExecutedBlocks(
	require *require.Assertions,
	parentID ids.ID,
	parentHeight uint64,
	parentTimestamp int64,
	timestampOffset int64,
	numBlocks int,
) []*chain.ExecutedBlock {
	return GenerateTestExecutedBlocks(
		require,
		parentID,
		ids.Empty.Prefix(1),
		parentHeight,
		parentTimestamp,
		timestampOffset,
		numBlocks,
		0,
	)
}

// GenerateTestExecutedBlocks creates [numBlocks] executed block for testing purposes, each with [numTx] transactions.
// these blocks are not semantically valid for execution, and are meant execlusively for serialization testing purposes.
func GenerateTestExecutedBlocks(
	require *require.Assertions,
	parentID ids.ID,
	chainID ids.ID,
	parentHeight uint64,
	parentTimestamp int64,
	timestampOffset int64,
	numBlocks int,
	numTxsPerBlock int,
) []*chain.ExecutedBlock {
	testBalanceHandler := balance.NewPrefixBalanceHandler([]byte{0})
	rules := genesis.NewDefaultRules()
	feeManager := internalfees.NewManager(nil)
	executedBlocks := make([]*chain.ExecutedBlock, numBlocks)
	parser := NewTestParser()
	ts := tstate.New(0)
	db, err := merkledb.New(
		context.Background(),
		memdb.New(),
		merkledb.Config{BranchFactor: merkledb.BranchFactor16},
	)
	require.NoError(err)

	tstateview := ts.NewView(state.CompletePermissions, db, 0)
	require.NoError(tstateview.Insert(context.Background(), testBalanceHandler.BalanceKey(NewDummyTestAuth().Sponsor()), binary.BigEndian.AppendUint64(nil, math.MaxUint64)))

	actions := NewDummyTestActions(numBlocks * numTxsPerBlock)
	for blockIndex := range executedBlocks {
		txs := make([]*chain.Transaction, numTxsPerBlock)
		blockTimestamp := parentTimestamp + timestampOffset*int64(blockIndex)
		base := chain.Base{
			Timestamp: blockTimestamp + 1,
			ChainID:   chainID,
			MaxFee:    math.MaxUint64,
		}
		for txIndex := range txs {
			actions := []chain.Action{actions[blockIndex*numTxsPerBlock+txIndex]}
			auth := NewDummyTestAuth()
			tx, err := chain.NewTransaction(base, actions, auth)
			require.NoError(err)
			txs = append(txs, tx)
		}

		statelessBlock, err := chain.NewStatelessBlock(
			parentID,
			blockTimestamp,
			parentHeight+1+uint64(blockIndex),
			txs,
			ids.Empty,
			nil,
		)
		require.NoError(err)
		parentID = statelessBlock.GetID()

		results := make([]*chain.Result, len(txs))
		for i, tx := range txs {
			result, err := tx.Execute(context.Background(), feeManager, testBalanceHandler, rules, tstateview, parentTimestamp+timestampOffset*int64(i))
			require.NoError(err)
			results[i] = result
		}

		// marshal and unmarshal to remove all cached fields (ie. tx state keys)
		blkBytes := statelessBlock.GetBytes()
		require.NoError(err)
		statelessBlock, err = chain.UnmarshalBlock(blkBytes, parser)
		require.NoError(err)

		blk := chain.NewExecutedBlock(
			statelessBlock,
			results,
			fees.Dimensions{}, // TODO: populate these fields correctly as needed for testing
			fees.Dimensions{},
		)
		executedBlocks[blockIndex] = blk
	}
	return executedBlocks
}
