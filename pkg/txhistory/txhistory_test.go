// Copyright © 2022 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package txhistory

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hyperledger/firefly-common/pkg/config"
	"github.com/hyperledger/firefly-common/pkg/fftypes"
	"github.com/hyperledger/firefly-transaction-manager/internal/tmconfig"
	"github.com/hyperledger/firefly-transaction-manager/pkg/apitypes"
	"github.com/stretchr/testify/assert"
)

func newTestTxHistoryManager(t *testing.T) (context.Context, *manager, func()) {
	tmconfig.Reset()
	ctx, cancelCtx := context.WithCancel(context.Background())
	h := NewTxHistoryManager(ctx).(*manager)
	return ctx, h, cancelCtx
}

func TestManagedTXSubStatus(t *testing.T) {
	mtx := &apitypes.ManagedTX{}
	ctx, h, done := newTestTxHistoryManager(t)
	defer done()

	// No sub-status entries initially
	assert.Nil(t, h.CurrentSubStatus(ctx, mtx))

	// Adding the same sub-status lots of times in succession should only result
	// in a single entry for that instance
	for i := 0; i < 100; i++ {
		h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusReceived)
	}

	assert.Equal(t, 1, len(mtx.History))
	assert.Equal(t, "Received", string(h.CurrentSubStatus(ctx, mtx).Status))

	// Adding a different type of sub-status should result in
	// a new entry in the list
	h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusTracking)

	assert.Equal(t, 2, len(mtx.History))

	// Even if many new types are added we shouldn't go over the
	// configured upper limit
	for i := 0; i < 100; i++ {
		h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusStale)
		h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusTracking)
	}
	assert.Equal(t, 50, len(mtx.History))

}

func TestManagedTXSubStatusRepeat(t *testing.T) {
	ctx, h, done := newTestTxHistoryManager(t)
	defer done()
	mtx := &apitypes.ManagedTX{}

	// Add a sub-status
	h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusReceived)
	assert.Equal(t, 1, len(mtx.History))
	assert.Equal(t, 1, len(mtx.HistorySummary))

	// Add another sub-status
	h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusTracking)
	assert.Equal(t, 2, len(mtx.History))
	assert.Equal(t, 2, len(mtx.HistorySummary))

	// Add another that we've seen before
	h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusReceived)
	assert.Equal(t, 3, len(mtx.History))        // This goes up
	assert.Equal(t, 2, len(mtx.HistorySummary)) // This doesn't
}

func TestManagedTXSubStatusAction(t *testing.T) {
	ctx, h, done := newTestTxHistoryManager(t)
	defer done()
	mtx := &apitypes.ManagedTX{}

	// Add at least 1 sub-status
	h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusReceived)

	// Add an action
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionAssignNonce, nil, nil)
	assert.Equal(t, 1, len(mtx.History[0].Actions))
	assert.Nil(t, mtx.History[0].Actions[0].LastErrorTime)

	// Add another action
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionRetrieveGasPrice, nil, fftypes.JSONAnyPtr(`{"gasError":"Acme Gas Oracle RC=12345"}`))
	assert.Equal(t, 2, len(mtx.History[0].Actions))
	assert.Equal(t, (*mtx.History[0].Actions[1].LastError).String(), `{"gasError":"Acme Gas Oracle RC=12345"}`)

	// Add the same action which should cause the previous one to inc its counter
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionRetrieveGasPrice, fftypes.JSONAnyPtr(`{"info":"helloworld"}`), fftypes.JSONAnyPtr(`{"error":"nogood"}`))
	assert.Equal(t, 2, len(mtx.History[0].Actions))
	assert.Equal(t, mtx.History[0].Actions[1].Action, apitypes.TxActionRetrieveGasPrice)
	assert.Equal(t, 2, mtx.History[0].Actions[1].Count)

	// Add the same action but with new error information should update the last error field
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionRetrieveGasPrice, nil, fftypes.JSONAnyPtr(`{"gasError":"Acme Gas Oracle RC=67890"}`))
	assert.Equal(t, 2, len(mtx.History[0].Actions))
	assert.NotNil(t, mtx.History[0].Actions[1].LastErrorTime)
	assert.Equal(t, (*mtx.History[0].Actions[1].LastError).String(), `{"gasError":"Acme Gas Oracle RC=67890"}`)

	// Add a new type of action
	reason := "known_transaction"
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionSubmitTransaction, fftypes.JSONAnyPtr(`{"reason":"`+reason+`"}`), nil)
	assert.Equal(t, 3, len(mtx.History[0].Actions))
	assert.Equal(t, mtx.History[0].Actions[2].Action, apitypes.TxActionSubmitTransaction)
	assert.Equal(t, 1, mtx.History[0].Actions[2].Count)
	assert.Nil(t, mtx.History[0].Actions[2].LastErrorTime)

	// Add one more type of action

	receiptId := "123456"
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionReceiveReceipt, fftypes.JSONAnyPtr(`{"receiptId":"`+receiptId+`"}`), nil)
	assert.Equal(t, 4, len(mtx.History[0].Actions))
	assert.Equal(t, mtx.History[0].Actions[3].Action, apitypes.TxActionReceiveReceipt)
	assert.Equal(t, 1, mtx.History[0].Actions[3].Count)
	assert.Nil(t, mtx.History[0].Actions[3].LastErrorTime)

	// History is the complete list of unique sub-status types and actions
	assert.Equal(t, 5, len(mtx.HistorySummary))

	// Add some new sub-status and actions to check max lengths are correct
	// Seen one of these before - should increase summary length by 1
	h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusConfirmed)
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionReceiveReceipt, fftypes.JSONAnyPtr(`{"receiptId":"`+receiptId+`"}`), nil)
	assert.Equal(t, 6, len(mtx.HistorySummary))

	// Seen both of these before - no change expected
	h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusReceived)
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionAssignNonce, nil, nil)
	assert.Equal(t, 6, len(mtx.HistorySummary))

	// Sanity check the history summary entries
	for _, historyEntry := range mtx.HistorySummary {
		assert.NotNil(t, historyEntry.FirstOccurrence)
		assert.NotNil(t, historyEntry.LastOccurrence)
		assert.GreaterOrEqual(t, historyEntry.Count, 1)

		if historyEntry.Action == apitypes.TxActionRetrieveGasPrice {
			// The first and last occurrence timestamps shoudn't be the same
			assert.NotEqual(t, historyEntry.FirstOccurrence, historyEntry.LastOccurrence)

			// We should have a count of 3 for this action
			assert.Equal(t, historyEntry.Count, 3)
		}
	}
}

func TestManagedTXSubStatusInvalidJSON(t *testing.T) {
	ctx, h, done := newTestTxHistoryManager(t)
	defer done()
	mtx := &apitypes.ManagedTX{}

	reason := "\"cannot-marshall\""

	// Add a new type of action
	h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusReceived)
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionSubmitTransaction, fftypes.JSONAnyPtr(`{"reason":"`+reason+`"}`), nil)
	val, err := json.Marshal(mtx.History[0].Actions[0].LastInfo)

	// It should never be possible to cause the sub-status history to become un-marshallable
	assert.NoError(t, err)
	assert.Contains(t, string(val), "cannot-marshall")

}

func TestManagedTXSubStatusMaxEntries(t *testing.T) {
	ctx, h, done := newTestTxHistoryManager(t)
	defer done()
	mtx := &apitypes.ManagedTX{}
	var nextSubStatus apitypes.TxSubStatus

	// Create 100 unique sub-status strings. We should only keep the
	// first 50
	for i := 0; i < 100; i++ {
		nextSubStatus = apitypes.TxSubStatus(fmt.Sprint(i))
		h.SetSubStatus(ctx, mtx, nextSubStatus)
	}

	assert.Equal(t, 50, len(mtx.History))

}

func TestMaxHistoryCountSetToZero(t *testing.T) {
	tmconfig.Reset()
	config.Set(tmconfig.TransactionsMaxHistoryCount, 0)
	ctx, cancelCtx := context.WithCancel(context.Background())
	h := NewTxHistoryManager(ctx).(*manager)
	defer cancelCtx()
	mtx := &apitypes.ManagedTX{}

	h.SetSubStatus(ctx, mtx, apitypes.TxSubStatusReceived)
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionSubmitTransaction, nil, nil)
	assert.Equal(t, 0, len(mtx.History))
	assert.Equal(t, 0, len(mtx.HistorySummary))

}

func TestAddReceivedStatusWhenNothingSet(t *testing.T) {
	ctx, h, done := newTestTxHistoryManager(t)

	defer done()
	mtx := &apitypes.ManagedTX{}

	assert.Equal(t, 0, len(mtx.History))
	h.AddSubStatusAction(ctx, mtx, apitypes.TxActionSubmitTransaction, nil, nil)
	assert.Equal(t, 1, len(mtx.History))
	assert.Equal(t, 1, len(mtx.History[0].Actions))
	assert.Equal(t, apitypes.TxSubStatusReceived, mtx.History[0].Status)
	assert.Equal(t, apitypes.TxActionSubmitTransaction, mtx.History[0].Actions[0].Action)
}
func TestJSONOrStringNull(t *testing.T) {
	assert.Nil(t, jsonOrString(nil))
}
