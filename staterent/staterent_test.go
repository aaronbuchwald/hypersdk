// Copyright (C) 2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package staterent

import (
	"testing"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/stretchr/testify/require"
)

func TestStateRent(t *testing.T) {
	require := require.New(t)

	s := NewStateRent()

	items := []ids.ID{
		ids.GenerateTestID(),
		ids.GenerateTestID(),
		ids.GenerateTestID(),
	}

	for i, item := range items {
		s.Add(item, uint64(i)+1)
	}

	require.Equal(uint64(1), s.GetBalance(items[0]))
	require.Equal(uint64(2), s.GetBalance(items[1]))
	require.Equal(uint64(3), s.GetBalance(items[2]))

	removedItems1 := s.AddCost(1)
	require.Len(removedItems1, 1)
	require.Equal(items[0], removedItems1[0])

	removedItems2 := s.AddCost(1)
	require.Len(removedItems2, 1)
	require.Equal(items[1], removedItems2[0])

	s.IncreaseBalance(items[2], 10)
	require.Equal(uint64(11), s.GetBalance(items[2]))

	removedItems3 := s.AddCost(1)
	require.Len(removedItems3, 0)
	slice := s.AddCost(100)
	require.Len(slice, 1)
	require.Equal(items[2], slice[0])
}
