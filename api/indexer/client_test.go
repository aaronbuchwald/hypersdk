// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package indexer

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/trace"
	"github.com/stretchr/testify/require"

	"github.com/ava-labs/hypersdk/api"
	"github.com/ava-labs/hypersdk/chain/chaintest"
)

func TestIndexerClientBlocks(t *testing.T) {
	const (
		numExecutedBlocks = 4
		blockWindow       = 2
		numTxs            = 3
	)

	testCases := []struct {
		name      string
		blkHeight uint64
		err       error
	}{
		{
			name:      "success",
			blkHeight: numExecutedBlocks - 1,
			err:       nil,
		},
		{
			name:      "missing block",
			blkHeight: 0,
			err:       errBlockNotFound,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx := context.Background()
			indexer, executedBlocks, _ := createTestIndexer(t, ctx, numExecutedBlocks, blockWindow, numTxs)
			expectedBlock := executedBlocks[tt.blkHeight]

			jsonHandler, err := api.NewJSONRPCHandler(Name, NewServer(trace.Noop, indexer))
			require.NoError(err)

			httpServer := httptest.NewServer(jsonHandler)
			t.Cleanup(func() {
				httpServer.Close()
			})

			parser := chaintest.NewTestParser()

			client := NewClient(httpServer.URL)
			actualBlock, err := client.GetBlockByHeight(ctx, expectedBlock.Block.Hght, parser)
			if tt.err == nil {
				require.NoError(err)
				require.Equal(expectedBlock.Block, actualBlock.Block)
			} else {
				require.ErrorContains(err, tt.err.Error()) //nolint:forbidigo
			}

			actualBlock, err = client.GetBlock(ctx, expectedBlock.Block.GetID(), parser)
			if tt.err == nil {
				require.NoError(err)
				require.Equal(expectedBlock.Block, actualBlock.Block)
			} else {
				require.ErrorContains(err, tt.err.Error()) //nolint:forbidigo
			}

			expectedLatestBlock := executedBlocks[numExecutedBlocks-1]
			actualLatestBlock, err := client.GetLatestBlock(ctx, parser)
			require.NoError(err)
			require.Equal(expectedLatestBlock.Block, actualLatestBlock.Block)
		})
	}
}

func TestIndexerClientTransactions(t *testing.T) {
	const (
		numExecutedBlocks = 4
		blockWindow       = 2
		numTxs            = 3
	)

	testCases := []struct {
		name       string
		txBlkIndex int
		txIndex    int
		err        error
		found      bool
	}{
		{
			name:       "success",
			txBlkIndex: numExecutedBlocks - 1,
			txIndex:    0,
			found:      true,
			err:        nil,
		},
		{
			name:       "missing transaction",
			txBlkIndex: 0,
			txIndex:    0,
			found:      false,
			err:        nil,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx := context.Background()
			indexer, executedBlocks, _ := createTestIndexer(t, ctx, numExecutedBlocks, blockWindow, numTxs)

			jsonHandler, err := api.NewJSONRPCHandler(Name, NewServer(trace.Noop, indexer))
			require.NoError(err)

			httpServer := httptest.NewServer(jsonHandler)
			t.Cleanup(func() {
				httpServer.Close()
			})

			parser := chaintest.NewTestParser()

			client := NewClient(httpServer.URL)

			expectedExecutedBlock := executedBlocks[tt.txBlkIndex]
			expectedTx := expectedExecutedBlock.Block.Txs[tt.txIndex]
			txResponse, found, err := client.GetTxResults(ctx, expectedTx.GetID())
			require.Equal(tt.err, err)
			require.Equal(tt.found, found)
			if tt.found {
				require.Equal(GetTxResponse{
					TxBytes:   expectedTx.Bytes(),
					Timestamp: expectedExecutedBlock.Block.Tmstmp,
					Result:    expectedExecutedBlock.ExecutionResults.Results[tt.txIndex],
				}, txResponse)
			}

			txResponse, actualTx, found, err := client.GetTx(ctx, expectedTx.GetID(), parser)
			require.Equal(tt.err, err)
			require.Equal(tt.found, found)
			if tt.found {
				require.Equal(GetTxResponse{
					TxBytes:   expectedTx.Bytes(),
					Timestamp: executedBlocks[tt.txBlkIndex].Block.Tmstmp,
					Result:    executedBlocks[tt.txBlkIndex].ExecutionResults.Results[tt.txIndex],
				}, txResponse)
				require.Equal(expectedTx, actualTx)
			}
		})
	}
}

func TestIndexerClientWaitForTransaction(t *testing.T) {
	const (
		numExecutedBlocks = 2
		blockWindow       = 1
		numTxs            = 3
	)
	require := require.New(t)
	ctx := context.Background()
	indexer, _, _ := createTestIndexer(t, ctx, numExecutedBlocks, blockWindow, numTxs)

	jsonHandler, err := api.NewJSONRPCHandler(Name, NewServer(trace.Noop, indexer))
	require.NoError(err)

	httpServer := httptest.NewServer(jsonHandler)
	t.Cleanup(func() {
		httpServer.Close()
	})

	client := NewClient(httpServer.URL)

	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, _, err = client.WaitForTransaction(timeoutCtx, 1*time.Millisecond, ids.GenerateTestID())
	require.ErrorIs(err, context.DeadlineExceeded)
}
