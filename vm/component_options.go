// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package vm

import (
	"context"
	"fmt"

	"github.com/ava-labs/avalanchego/api/metrics"
	"github.com/ava-labs/hypersdk/api"
	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/event"
	"github.com/ava-labs/hypersdk/internal/builder"
	"github.com/ava-labs/hypersdk/internal/gossiper"
)

func WithManual() Option {
	return NewOption[struct{}](
		"manual",
		struct{}{},
		func(_ api.VM, _ struct{}) (Opt, error) {
			return newFuncOption(func(o *Options) {
				o.builderF = manualBuilder
				o.gossiperF = manualGossiper
			}), nil
		},
	)
}

func registerStartBuilder(vm *VM, builder builder.Builder) {
	vm.RegisterOnNormalOp(func() error {
		builder.Start()
		vm.AddCloser("builder", func() error {
			builder.Done()
			return nil
		})
		return nil
	})
	vm.RegisterMempoolUpdated(builder.Queue)
}

func manualBuilder(vm *VM) builder.Builder {
	builder := builder.NewManual(vm.snowInput.ToEngine, vm.snowCtx.Log)
	registerStartBuilder(vm, builder)
	return builder
}

func timeBuilder(vm *VM) builder.Builder {
	builder := builder.NewTime(vm.snowInput.ToEngine, vm.snowCtx.Log, vm.Mempool, func(ctx context.Context, t int64) (int64, int64, error) {
		blk, err := vm.consensusIndex.GetPreferredBlock(ctx)
		if err != nil {
			return 0, 0, err
		}
		return blk.Tmstmp, vm.ruleFactory.GetRules(t).GetMinBlockGap(), nil
	})
	registerStartBuilder(vm, builder)
	return builder
}

func registerStartGossiper(vm *VM, vmGossiper gossiper.Gossiper) error {
	vm.RegisterOnNormalOp(func() error {
		vmGossiper.Start(vm.network.NewClient(txGossipHandlerID))
		vm.AddCloser("gossiper", func() error {
			vmGossiper.Done()
			return nil
		})
		return nil
	})

	if err := vm.network.AddHandler(
		txGossipHandlerID,
		gossiper.NewTxGossipHandler(
			vm.snowCtx.Log,
			vmGossiper,
		),
	); err != nil {
		return fmt.Errorf("failed to add tx gossip handler: %w", err)
	}
	vm.RegisterMempoolUpdated(vmGossiper.Queue)
	return nil
}

func manualGossiper(vm *VM) (gossiper.Gossiper, error) {
	gossipRegistry, err := metrics.MakeAndRegister(vm.snowCtx.Metrics, gossiperNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to register %s metrics: %w", gossiperNamespace, err)
	}
	gossiper, err := gossiper.NewManual[*chain.Transaction](
		vm.snowCtx.Log,
		gossipRegistry,
		vm.Mempool,
		&chain.TxSerializer{
			ActionRegistry: vm.actionCodec,
			AuthRegistry:   vm.authCodec,
		},
		vm,
		vm.config.TargetGossipDuration,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create manual gossiper: %w", err)
	}
	return gossiper, registerStartGossiper(vm, gossiper)
}

func proposerGossiper(vm *VM) (gossiper.Gossiper, error) {
	gossipRegistry, err := metrics.MakeAndRegister(vm.snowCtx.Metrics, gossiperNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to register %s metrics: %w", gossiperNamespace, err)
	}
	txGossiper, err := gossiper.NewTarget[*chain.Transaction](
		vm.tracer,
		vm.snowCtx.Log,
		gossipRegistry,
		vm.Mempool,
		&chain.TxSerializer{
			ActionRegistry: vm.actionCodec,
			AuthRegistry:   vm.authCodec,
		},
		vm,
		vm,
		vm.config.TargetGossipDuration, // TODO: separate to its own config
		&gossiper.TargetProposers[*chain.Transaction]{
			Validators: vm,
			Config:     gossiper.DefaultTargetProposerConfig(),
		},
		gossiper.DefaultTargetConfig(),
		vm.snowInput.Shutdown,
	)
	if err != nil {
		return nil, err
	}
	vm.snowApp.AddVerifiedSub(event.SubscriptionFunc[*chain.OutputBlock]{
		NotifyF: func(_ context.Context, b *chain.OutputBlock) error {
			txGossiper.BlockVerified(b.GetTimestamp())
			return nil
		},
	})
	return txGossiper, registerStartGossiper(vm, txGossiper)
}
