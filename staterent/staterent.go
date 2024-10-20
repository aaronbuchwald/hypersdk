// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package staterent

import (
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/hypersdk/internal/heap"
)

// TODO: switch from single slot to multiple slots
// type StateRent interface {
// 	AddCost(uint64) []ids.ID
// 	Add(items []ids.ID, initialBalance uint64)
// 	Remove(items []ids.ID) (uint64, error)
// 	IncreaseBalance(items []ids.ID, amount uint64)
// 	GetBalance(item ids.ID) uint64
// }

type stateRent struct {
	totalCost uint64
	items     *heap.Heap[ids.ID, uint64]
}

func NewStateRent() *stateRent {
	return &stateRent{
		items: heap.New[ids.ID, uint64](0, true),
	}
}

func (s *stateRent) AddCost(cost uint64) []ids.ID {
	s.totalCost += cost

	removed := make([]ids.ID, 0)
	for {
		next := s.items.First()
		if next == nil {
			break
		}
		if next.Val > s.totalCost {
			break
		}
		_ = s.items.Pop()
		removed = append(removed, next.ID)
	}

	return removed
}

func (s *stateRent) Add(item ids.ID, initialBalance uint64) {
	s.items.Push(&heap.Entry[ids.ID, uint64]{
		ID:    item,
		Item:  item,
		Val:   initialBalance + s.totalCost,
		Index: s.items.Len(),
	})
}

func (s *stateRent) Remove(item ids.ID) uint64 {
	entry, ok := s.items.Get(item)
	if !ok {
		return 0
	}
	s.items.Remove(entry.Index)
	return entry.Val - s.totalCost
}

func (s *stateRent) IncreaseBalance(item ids.ID, amount uint64) {
	entry, ok := s.items.Get(item)
	if !ok {
		return
	}
	// TODO: switch from Remove + Push to single Fix call
	s.items.Remove(entry.Index)
	entry.Val += amount
	s.items.Push(entry)
}

func (s *stateRent) GetBalance(item ids.ID) uint64 {
	entry, ok := s.items.Get(item)
	if !ok {
		return 0
	}
	return entry.Val - s.totalCost
}
