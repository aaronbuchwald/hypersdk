// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package vm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ava-labs/hypersdk/api"
	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/chainindex"
	"github.com/ava-labs/hypersdk/event"
	"github.com/ava-labs/hypersdk/internal/builder"
	"github.com/ava-labs/hypersdk/internal/gossiper"
	"github.com/ava-labs/hypersdk/internal/mempool"
	"github.com/ava-labs/hypersdk/snow"
	"github.com/ava-labs/hypersdk/statesync"
)

type ComponentOptions[I snow.Block, O snow.Block, A snow.Block] struct {
	// init is a function that provides a config and basic backend dependencies for registration
}

type Options struct {
	mempoolF        func(vm *VM) *mempool.Mempool[*chain.Transaction]

	// both depend on mempool
	// mempool, builder, gossiper must be exported for testing
	builderF        func(vm *VM) builder.Builder
	gossiperF       func(vm *VM) (gossiper.Gossiper, error)
	

	syncers         func(vm *VM) ([]statesync.Syncer[*chain.ExecutionBlock], error)
	loadLatestChain func(vm *VM) (*chainindex.ChainIndex[*chain.ExecutionBlock], *chain.OutputBlock, *chain.OutputBlock, error)
	loadChain       func(vm *VM) (snow.ChainHandler[*chain.ExecutionBlock, *chain.OutputBlock, *chain.OutputBlock], error)


	onNormalOp                 []func() error
	onMempoolUpdated           []func(context.Context)
	apiOptions                 []Option
	blockSubscriptionFactories []event.SubscriptionFactory[*chain.ExecutedBlock]
	vmAPIHandlerFactories      []api.HandlerFactory[api.VM]
}

// what do I need to change?
// gossiper
// syncers
// loadLatestChain
// loadChain

// can I build a version of the current VM that is clean and assembled as a set of these components
// via options and interfaces?
// then I just need to implement the DSMR equivalent

func NewOptions(ops ...Option) *Options {
	return &Options{
		syncers:    registerSyncers,
		builderF:   timeBuilder,
		gossiperF:  proposerGossiper,
		mempoolF:   createMempool,
		apiOptions: ops,
	}
}

type optionFunc func(vm api.VM, configBytes []byte) (Opt, error)

type OptionFunc[T any] func(vm api.VM, config T) (Opt, error)

type Option struct {
	Namespace  string
	optionFunc optionFunc
}

func newOptionWithBytes(namespace string, optionFunc optionFunc) Option {
	return Option{
		Namespace:  namespace,
		optionFunc: optionFunc,
	}
}

// NewOption returns an option with:
// 1) A namespace to define the key in the VM's JSON config that should be supplied to this option
// 2) A default config value the VM will directly unmarshal into
// 3) An option function that takes the VM and resulting config value as arguments
func NewOption[T any](namespace string, defaultConfig T, optionFunc OptionFunc[T]) Option {
	config := defaultConfig
	configOptionFunc := func(vm api.VM, configBytes []byte) (Opt, error) {
		if len(configBytes) > 0 {
			if err := json.Unmarshal(configBytes, &config); err != nil {
				return nil, fmt.Errorf("failed to unmarshal %q config %q: %w", namespace, string(configBytes), err)
			}
		}

		return optionFunc(vm, config)
	}
	return newOptionWithBytes(namespace, configOptionFunc)
}

func WithBlockSubscriptions(subscriptions ...event.SubscriptionFactory[*chain.ExecutedBlock]) Opt {
	return newFuncOption(func(o *Options) {
		o.blockSubscriptionFactories = append(o.blockSubscriptionFactories, subscriptions...)
	})
}

func WithVMAPIs(apiHandlerFactories ...api.HandlerFactory[api.VM]) Opt {
	return newFuncOption(func(o *Options) {
		o.vmAPIHandlerFactories = append(o.vmAPIHandlerFactories, apiHandlerFactories...)
	})
}

type Opt interface {
	apply(*Options)
}

// NewOpt mixes a list of Opt in a new one Opt.
func NewOpt(opts ...Opt) Opt {
	return newFuncOption(func(o *Options) {
		for _, opt := range opts {
			opt.apply(o)
		}
	})
}

type funcOption struct {
	f func(*Options)
}

func (fdo *funcOption) apply(do *Options) {
	fdo.f(do)
}

func newFuncOption(f func(*Options)) *funcOption {
	return &funcOption{
		f: f,
	}
}
