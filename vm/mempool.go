// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package vm

import (
	"context"

	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/event"
	"github.com/ava-labs/hypersdk/internal/mempool"
	"go.uber.org/zap"
)

func createMempool(vm *VM) *mempool.Mempool[*chain.Transaction] {
	mempool := mempool.New[*chain.Transaction](vm.tracer, vm.config.MempoolSize, vm.config.MempoolSponsorSize)
	vm.snowApp.AddAcceptedSub(event.SubscriptionFunc[*chain.OutputBlock]{
		NotifyF: func(ctx context.Context, b *chain.OutputBlock) error {
			droppedTxs := mempool.SetMinTimestamp(ctx, b.Tmstmp)
			vm.snowCtx.Log.Debug("dropping expired transactions from mempool",
				zap.Stringer("blkID", b.GetID()),
				zap.Int("numTxs", len(droppedTxs)),
			)
			return nil
		},
	})
	vm.snowApp.AddVerifiedSub(event.SubscriptionFunc[*chain.OutputBlock]{
		NotifyF: func(ctx context.Context, b *chain.OutputBlock) error {
			mempool.Remove(ctx, b.StatelessBlock.Txs)
			return nil
		},
	})
	vm.snowApp.AddRejectedSub(event.SubscriptionFunc[*chain.OutputBlock]{
		NotifyF: func(ctx context.Context, b *chain.OutputBlock) error {
			mempool.Add(ctx, b.StatelessBlock.Txs)
			return nil
		},
	})
	return mempool
}
