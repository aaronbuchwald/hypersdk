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

	removedItems1 := s.AddCost(1)
	require.Len(removedItems1, 1)
	require.Equal(items[0], removedItems1[0])

	removedItems2 := s.AddCost(1)
	require.Len(removedItems2, 1)
	require.Equal(items[1], removedItems2[0])

	removedItems3 := s.AddCost(1)
	require.Len(removedItems3, 1)
	require.Equal(items[2], removedItems3[0])
}

// Test cases:
// Removing 