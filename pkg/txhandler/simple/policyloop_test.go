// Copyright © 2023 Kaleido, Inc.
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

package simple

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/hyperledger/firefly-common/pkg/fftypes"
	// Internal packages are used in the tests for e2e tests with more coverage
	// If you are developing a customized transaction handler, you'll need to mock the toolkit APIs instead
	"github.com/hyperledger/firefly-transaction-manager/internal/confirmations"
	"github.com/hyperledger/firefly-transaction-manager/internal/persistence"
	"github.com/hyperledger/firefly-transaction-manager/mocks/confirmationsmocks"
	"github.com/hyperledger/firefly-transaction-manager/mocks/ffcapimocks"
	"github.com/hyperledger/firefly-transaction-manager/mocks/metricsmocks"
	"github.com/hyperledger/firefly-transaction-manager/mocks/persistencemocks"
	"github.com/hyperledger/firefly-transaction-manager/mocks/wsmocks"
	"github.com/hyperledger/firefly-transaction-manager/pkg/apitypes"
	"github.com/hyperledger/firefly-transaction-manager/pkg/ffcapi"
	"github.com/hyperledger/firefly-transaction-manager/pkg/fftm"
	"github.com/hyperledger/firefly-transaction-manager/pkg/txhistory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func sendSampleTX(t *testing.T, sth *simpleTransactionHandler, signer string, nonce int64) *apitypes.ManagedTX {

	txInput := ffcapi.TransactionInput{
		TransactionHeaders: ffcapi.TransactionHeaders{
			From: signer,
		},
	}
	ctx := context.Background()
	mfc := sth.toolkit.Connector.(*ffcapimocks.API)
	mfc.On("NextNonceForSigner", ctx, &ffcapi.NextNonceForSignerRequest{
		Signer: signer,
	}).Return(&ffcapi.NextNonceForSignerResponse{
		Nonce: fftypes.NewFFBigInt(nonce),
	}, ffcapi.ErrorReason(""), nil).Once()
	mfc.On("TransactionPrepare", ctx, &ffcapi.TransactionPrepareRequest{
		TransactionInput: txInput,
	}).Return(&ffcapi.TransactionPrepareResponse{
		Gas:             fftypes.NewFFBigInt(100000),
		TransactionData: "0xabce1234",
	}, ffcapi.ErrorReason(""), nil).Once()

	mtx, err := sth.HandleNewTransaction(ctx, &apitypes.TransactionRequest{
		TransactionInput: txInput,
	})
	assert.NoError(t, err)
	return mtx
}

func sendSampleDeployment(t *testing.T, sth *simpleTransactionHandler, signer string, nonce int64) *apitypes.ManagedTX {

	ctx := context.Background()
	mfc := sth.toolkit.Connector.(*ffcapimocks.API)
	contractDeployPrepareRequest := ffcapi.ContractDeployPrepareRequest{
		TransactionHeaders: ffcapi.TransactionHeaders{
			From: signer,
		},
		Contract: fftypes.JSONAnyPtrBytes([]byte("0xfeedbeef")),
	}
	mfc.On("NextNonceForSigner", ctx, &ffcapi.NextNonceForSignerRequest{
		Signer: signer,
	}).Return(&ffcapi.NextNonceForSignerResponse{
		Nonce: fftypes.NewFFBigInt(nonce),
	}, ffcapi.ErrorReason(""), nil).Once()
	mfc.On("DeployContractPrepare", ctx, mock.Anything).Return(&ffcapi.TransactionPrepareResponse{
		Gas:             fftypes.NewFFBigInt(100000),
		TransactionData: "0xabce1234",
	}, ffcapi.ErrorReason(""), nil).Once()

	mtx, err := sth.HandleNewContractDeployment(ctx, &apitypes.ContractDeployRequest{
		ContractDeployPrepareRequest: contractDeployPrepareRequest,
	})
	assert.NoError(t, err)
	return mtx
}

func TestPolicyLoopE2EOk(t *testing.T) {
	f, tk, _, conf, cleanup := newTestTransactionHandlerFactoryWithFilePersistence(t)
	defer cleanup()
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)
	txHash := "0x" + fftypes.NewRandB32().String()

	mfc := sth.toolkit.Connector.(*ffcapimocks.API)
	mfc.On("TransactionSend", sth.ctx, mock.MatchedBy(func(r *ffcapi.TransactionSendRequest) bool {
		return r.Nonce.Equals(fftypes.NewFFBigInt(12345))
	})).Return(&ffcapi.TransactionSendResponse{
		TransactionHash: txHash,
	}, ffcapi.ErrorReason(""), nil)

	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}

	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		return n.NotificationType == confirmations.NewTransaction
	})).Run(func(args mock.Arguments) {
		n := args[0].(*confirmations.Notification)
		n.Transaction.Receipt(context.Background(), &ffcapi.TransactionReceiptResponse{
			BlockNumber:      fftypes.NewFFBigInt(12345),
			TransactionIndex: fftypes.NewFFBigInt(10),
			BlockHash:        fftypes.NewRandB32().String(),
			ProtocolID:       fmt.Sprintf("%.12d/%.6d", fftypes.NewFFBigInt(12345).Int64(), fftypes.NewFFBigInt(10).Int64()),
			Success:          true,
			ContractLocation: fftypes.JSONAnyPtr(`{"address": "0x24746b95d118b2b4e8d07b06b1bad988fbf9415d"}`),
		})
		n.Transaction.Confirmed(context.Background(), []apitypes.BlockInfo{})
	}).Return(nil)
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(nil).Maybe()

	eh.WsServer = mws
	sth.toolkit.EventHandler = eh

	mtx := sendSampleTX(t, sth, "0xaaaaa", 12345)
	// Run the policy once to do the send
	<-sth.inflightStale // from sending the TX
	sth.policyLoopCycle(sth.ctx, true)
	assert.Equal(t, mtx.ID, sth.inflight[0].mtx.ID)
	assert.Equal(t, apitypes.TxStatusPending, sth.inflight[0].mtx.Status)

	// A second time will mark it complete for flush
	sth.policyLoopCycle(sth.ctx, false)

	<-sth.inflightStale // policy loop should have marked us stale, to clean up the TX
	sth.policyLoopCycle(sth.ctx, true)
	assert.Empty(t, sth.inflight)

	// Check the update is persisted
	rtx, err := sth.toolkit.TXPersistence.GetTransactionByID(sth.ctx, mtx.ID)
	assert.NoError(t, err)
	assert.Equal(t, apitypes.TxStatusSucceeded, rtx.Status)

	mc.AssertExpectations(t)
	mfc.AssertExpectations(t)
}

func TestPolicyLoopIgnoreTransactionInformationalEventHandlingErrors(t *testing.T) {
	f, tk, _, conf, cleanup := newTestTransactionHandlerFactoryWithFilePersistence(t)
	defer cleanup()
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)
	txHash := "0x" + fftypes.NewRandB32().String()

	mfc := sth.toolkit.Connector.(*ffcapimocks.API)
	mfc.On("TransactionSend", sth.ctx, mock.MatchedBy(func(r *ffcapi.TransactionSendRequest) bool {
		return r.Nonce.Equals(fftypes.NewFFBigInt(12345))
	})).Return(&ffcapi.TransactionSendResponse{
		TransactionHash: txHash,
	}, ffcapi.ErrorReason(""), nil)

	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}

	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		return n.NotificationType == confirmations.NewTransaction
	})).Run(func(args mock.Arguments) {
		sth.inflight = []*pendingState{}
		n := args[0].(*confirmations.Notification)
		n.Transaction.Receipt(context.Background(), &ffcapi.TransactionReceiptResponse{
			BlockNumber:      fftypes.NewFFBigInt(12345),
			TransactionIndex: fftypes.NewFFBigInt(10),
			BlockHash:        fftypes.NewRandB32().String(),
			ProtocolID:       fmt.Sprintf("%.12d/%.6d", fftypes.NewFFBigInt(12345).Int64(), fftypes.NewFFBigInt(10).Int64()),
			Success:          true,
			ContractLocation: fftypes.JSONAnyPtr(`{"address": "0x24746b95d118b2b4e8d07b06b1bad988fbf9415d"}`),
		})
		n.Transaction.Confirmed(context.Background(), []apitypes.BlockInfo{})
	}).Return(nil)
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(nil).Maybe()

	eh.WsServer = mws
	sth.toolkit.EventHandler = eh

	_ = sendSampleDeployment(t, sth, "0xaaaaa", 12345)
	// Run the policy once to do the send
	<-sth.inflightStale // from sending the TX
	sth.policyLoopCycle(sth.ctx, true)
	assert.Equal(t, 0, len(sth.inflight))

	mc.AssertExpectations(t)
	mfc.AssertExpectations(t)
}

func TestTransactionPreparationErrors(t *testing.T) {
	f, tk, _, conf, cleanup := newTestTransactionHandlerFactoryWithFilePersistence(t)
	defer cleanup()
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)
	mfc := sth.toolkit.Connector.(*ffcapimocks.API)

	txInput := ffcapi.TransactionInput{
		TransactionHeaders: ffcapi.TransactionHeaders{
			From: "0x000",
		},
	}
	ctx := context.Background()
	mfc.On("TransactionPrepare", ctx, &ffcapi.TransactionPrepareRequest{
		TransactionInput: txInput,
	}).Return(nil, ffcapi.ErrorReason(""), fmt.Errorf("txPop")).Once()
	mfc.On("DeployContractPrepare", ctx, mock.Anything).Return(nil, ffcapi.ErrorReason(""), fmt.Errorf("contractPop")).Once()

	_, err = sth.HandleNewTransaction(ctx, &apitypes.TransactionRequest{
		TransactionInput: txInput,
	})
	assert.Equal(t, "txPop", err.Error())

	contractDeployPrepareRequest := ffcapi.ContractDeployPrepareRequest{
		TransactionHeaders: ffcapi.TransactionHeaders{
			From: "0x000",
		},
		Contract: fftypes.JSONAnyPtrBytes([]byte("0xfeedbeef")),
	}
	_, err = sth.HandleNewContractDeployment(ctx, &apitypes.ContractDeployRequest{
		ContractDeployPrepareRequest: contractDeployPrepareRequest,
	})
	assert.Equal(t, "contractPop", err.Error())
	mfc.AssertExpectations(t)
}

func TestPolicyLoopE2EReverted(t *testing.T) {

	f, tk, _, conf, cleanup := newTestTransactionHandlerFactoryWithFilePersistence(t)
	defer cleanup()
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	mmm := &metricsmocks.TransactionHandlerMetrics{}
	mmm.On("InitTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightUsed, metricsGaugeTransactionsInflightUsedDescription, false).Return(nil).Maybe()
	mmm.On("InitTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightFree, metricsGaugeTransactionsInflightFreeDescription, false).Return(nil).Maybe()
	mmm.On("InitTxHandlerCounterMetricWithLabels", mock.Anything, metricsCounterTransactionProcessOperationsTotal, metricsCounterTransactionProcessOperationsTotalDescription, []string{metricsLabelNameOperation}, true).Return(nil).Maybe()
	mmm.On("InitTxHandlerHistogramMetricWithLabels", mock.Anything, metricsHistogramTransactionProcessOperationsDuration, metricsHistogramTransactionProcessOperationsDurationDescription, []float64{}, []string{metricsLabelNameOperation}, true).Return(nil).Maybe()
	mmm.On("SetTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightUsed, mock.Anything, mock.Anything).Return().Maybe()
	mmm.On("SetTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightFree, mock.Anything, mock.Anything).Return().Maybe()
	mmm.On("IncTxHandlerCounterMetricWithLabels", mock.Anything, metricsCounterTransactionProcessOperationsTotal, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	mmm.On("ObserveTxHandlerHistogramMetricWithLabels", mock.Anything, metricsHistogramTransactionProcessOperationsDuration, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()

	tk.MetricsManager = mmm

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)

	txHash := "0x" + fftypes.NewRandB32().String()

	mfc := sth.toolkit.Connector.(*ffcapimocks.API)
	mfc.On("TransactionSend", sth.ctx, mock.MatchedBy(func(r *ffcapi.TransactionSendRequest) bool {
		return r.Nonce.Equals(fftypes.NewFFBigInt(12345))
	})).Return(&ffcapi.TransactionSendResponse{
		TransactionHash: txHash,
	}, ffcapi.ErrorReason(""), nil)

	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}
	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		return n.NotificationType == confirmations.NewTransaction
	})).Run(func(args mock.Arguments) {
		n := args[0].(*confirmations.Notification)
		n.Transaction.Receipt(context.Background(), &ffcapi.TransactionReceiptResponse{
			BlockNumber:      fftypes.NewFFBigInt(12345),
			TransactionIndex: fftypes.NewFFBigInt(10),
			BlockHash:        fftypes.NewRandB32().String(),
			ProtocolID:       fmt.Sprintf("%.12d/%.6d", fftypes.NewFFBigInt(12345).Int64(), fftypes.NewFFBigInt(10).Int64()),
			Success:          false,
		})
		n.Transaction.Confirmed(context.Background(), []apitypes.BlockInfo{})
	}).Return(nil)
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(nil).Maybe()

	eh.WsServer = mws
	sth.toolkit.EventHandler = eh

	mtx := sendSampleTX(t, sth, "0xaaaaa", 12345)
	// Run the policy once to do the send
	<-sth.inflightStale // from sending the TX
	sth.policyLoopCycle(sth.ctx, true)
	assert.Equal(t, mtx.ID, sth.inflight[0].mtx.ID)
	assert.Equal(t, apitypes.TxStatusPending, sth.inflight[0].mtx.Status)

	// A second time will mark it complete for flush
	sth.policyLoopCycle(sth.ctx, false)

	<-sth.inflightStale // policy loop should have marked us stale, to clean up the TX
	sth.policyLoopCycle(sth.ctx, true)
	assert.Empty(t, sth.inflight)

	// Check the update is persisted
	rtx, err := sth.toolkit.TXPersistence.GetTransactionByID(sth.ctx, mtx.ID)
	assert.NoError(t, err)
	assert.Equal(t, apitypes.TxStatusFailed, rtx.Status)

	mc.AssertExpectations(t)
	mfc.AssertExpectations(t)
}

func TestPolicyLoopResubmitNewTXID(t *testing.T) {
	f, tk, _, conf, cleanup := newTestTransactionHandlerFactoryWithFilePersistence(t)
	defer cleanup()
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	conf.Set(Interval, "0")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)
	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)

	txHash1 := "0x" + fftypes.NewRandB32().String()
	txHash2 := "0x" + fftypes.NewRandB32().String()
	mfc := sth.toolkit.Connector.(*ffcapimocks.API)

	mfc.On("TransactionSend", mock.Anything, mock.Anything).Return(&ffcapi.TransactionSendResponse{
		TransactionHash: txHash1,
	}, ffcapi.ErrorReason(""), nil).Once()
	mfc.On("TransactionSend", mock.Anything, mock.Anything).Return(&ffcapi.TransactionSendResponse{
		TransactionHash: txHash2,
	}, ffcapi.ErrorReason(""), nil).Once()
	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}

	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		// First we get notified to add the old TX hash
		return n.NotificationType == confirmations.NewTransaction &&
			n.Transaction.TransactionHash == txHash1
	})).Return(nil)
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		// Then we get notified to remove the old TX hash
		return n.NotificationType == confirmations.RemovedTransaction &&
			n.Transaction.TransactionHash == txHash1
	})).Return(nil)
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		// Then we get the new TX hash, which we confirm
		return n.NotificationType == confirmations.NewTransaction &&
			n.Transaction.TransactionHash == txHash2
	})).Run(func(args mock.Arguments) {
		n := args[0].(*confirmations.Notification)
		n.Transaction.Receipt(context.Background(), &ffcapi.TransactionReceiptResponse{
			BlockNumber:      fftypes.NewFFBigInt(12345),
			TransactionIndex: fftypes.NewFFBigInt(10),
			BlockHash:        fftypes.NewRandB32().String(),
			ProtocolID:       fmt.Sprintf("%.12d/%.6d", fftypes.NewFFBigInt(12345).Int64(), fftypes.NewFFBigInt(10).Int64()),
			Success:          true,
		})
		n.Transaction.Confirmed(context.Background(), []apitypes.BlockInfo{})
	}).Return(nil)
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(nil).Maybe()

	eh.WsServer = mws
	sth.toolkit.EventHandler = eh

	mtx := sendSampleTX(t, sth, "0xaaaaa", 12345)

	// Run the policy once to do the send with the first hash
	<-sth.inflightStale // from sending the TX
	sth.policyLoopCycle(sth.ctx, true)
	assert.Len(t, sth.inflight, 1)
	assert.Equal(t, mtx.ID, sth.inflight[0].mtx.ID)
	assert.Equal(t, apitypes.TxStatusPending, sth.inflight[0].mtx.Status)
	assert.Equal(t, txHash1, sth.inflight[0].mtx.TransactionHash)

	// Run again to confirm it does not change anything, when the state is the same
	sth.policyLoopCycle(sth.ctx, true)
	assert.Len(t, sth.inflight, 1)
	assert.Equal(t, mtx.ID, sth.inflight[0].mtx.ID)
	assert.Equal(t, apitypes.TxStatusPending, sth.inflight[0].mtx.Status)
	assert.Equal(t, txHash1, sth.inflight[0].mtx.TransactionHash)

	// Reset the transaction so the policy manager resubmits it
	sth.inflight[0].mtx.FirstSubmit = nil
	sth.policyLoopCycle(sth.ctx, false)
	assert.Equal(t, mtx.ID, sth.inflight[0].mtx.ID)
	assert.Equal(t, apitypes.TxStatusPending, sth.inflight[0].mtx.Status)
	assert.Equal(t, txHash2, sth.inflight[0].mtx.TransactionHash)

	mc.AssertExpectations(t)
	mfc.AssertExpectations(t)
}

func TestNotifyConfirmationMgrFail(t *testing.T) {

	f, tk, _, conf, cleanup := newTestTransactionHandlerFactoryWithFilePersistence(t)
	defer cleanup()
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	mmm := &metricsmocks.TransactionHandlerMetrics{}
	mmm.On("InitTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightUsed, metricsGaugeTransactionsInflightUsedDescription, false).Return(nil).Maybe()
	mmm.On("InitTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightFree, metricsGaugeTransactionsInflightFreeDescription, false).Return(nil).Maybe()
	mmm.On("InitTxHandlerCounterMetricWithLabels", mock.Anything, metricsCounterTransactionProcessOperationsTotal, metricsCounterTransactionProcessOperationsTotalDescription, []string{metricsLabelNameOperation}, true).Return(nil).Maybe()
	mmm.On("InitTxHandlerHistogramMetricWithLabels", mock.Anything, metricsHistogramTransactionProcessOperationsDuration, metricsHistogramTransactionProcessOperationsDurationDescription, []float64{}, []string{metricsLabelNameOperation}, true).Return(nil).Maybe()
	mmm.On("SetTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightUsed, mock.Anything, mock.Anything).Return().Maybe()
	mmm.On("SetTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightFree, mock.Anything, mock.Anything).Return().Maybe()
	mmm.On("IncTxHandlerCounterMetricWithLabels", mock.Anything, metricsCounterTransactionProcessOperationsTotal, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	mmm.On("ObserveTxHandlerHistogramMetricWithLabels", mock.Anything, metricsHistogramTransactionProcessOperationsDuration, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()

	tk.MetricsManager = mmm
	sth := th.(*simpleTransactionHandler)
	sth.policyLoopInterval = 0
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)

	txHash := "0x" + fftypes.NewRandB32().String()

	removedTxHash := "0x" + fftypes.NewRandB32().String()

	mfc := sth.toolkit.Connector.(*ffcapimocks.API)
	mfc.On("TransactionSend", mock.Anything, mock.Anything).Return(&ffcapi.TransactionSendResponse{
		TransactionHash: txHash,
	}, ffcapi.ErrorReason(""), nil).Once()

	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}
	confirmation1Complete := make(chan struct{})
	confirmation2Complete := make(chan struct{})
	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		close(confirmation1Complete)
		// Then we get the new TX hash, which we confirm
		return n.NotificationType == confirmations.NewTransaction &&
			n.Transaction.TransactionHash == txHash
	})).Return(fmt.Errorf("pop")).Once()
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		// Then we get notified to remove the old TX hash
		return n.NotificationType == confirmations.RemovedTransaction &&
			n.Transaction.TransactionHash == removedTxHash
	})).Return(fmt.Errorf("pop")).Once()
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		close(confirmation2Complete)
		// Then we get the new TX hash, which we confirm
		return n.NotificationType == confirmations.NewTransaction &&
			n.Transaction.TransactionHash == txHash
	})).Return(fmt.Errorf("pop")).Once()
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(nil).Maybe()

	eh.WsServer = mws
	sth.toolkit.EventHandler = eh

	_ = sendSampleTX(t, sth, "0xaaaaa", 12345)

	// should emit 1 event to confirmation manager
	sth.policyLoopCycle(sth.ctx, true)

	<-confirmation1Complete

	// set a tracking transaction hash to test removal
	sth.inflight[0].trackingTransactionHash = removedTxHash

	// should emit 2 events to confirmation manager
	sth.policyLoopCycle(sth.ctx, false)
	<-confirmation2Complete

	mc.AssertExpectations(t)
	mfc.AssertExpectations(t)

}

func TestInflightSetListFailCancel(t *testing.T) {

	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)

	ctx, cancel := context.WithCancel(context.Background())
	sth.ctx = ctx
	sth.Init(sth.ctx, tk)
	cancel()
	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("ListTransactionsPending", sth.ctx, "", sth.maxInFlight, persistence.SortDirectionAscending).
		Return(nil, fmt.Errorf("pop"))

	sth.policyLoopCycle(sth.ctx, true)

	mp.AssertExpectations(t)

}

func TestPolicyLoopUpdateFail(t *testing.T) {

	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)

	txHash := "0x" + fftypes.NewRandB32().String()

	mfc := sth.toolkit.Connector.(*ffcapimocks.API)
	mfc.On("TransactionSend", sth.ctx, mock.MatchedBy(func(r *ffcapi.TransactionSendRequest) bool {
		return r.Nonce.Equals(fftypes.NewFFBigInt(1000))
	})).Return(&ffcapi.TransactionSendResponse{
		TransactionHash: txHash,
	}, ffcapi.ErrorReason(""), nil)

	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}
	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		return n.NotificationType == confirmations.NewTransaction
	})).Run(func(args mock.Arguments) {
		n := args[0].(*confirmations.Notification)
		n.Transaction.Receipt(context.Background(), &ffcapi.TransactionReceiptResponse{
			BlockNumber:      fftypes.NewFFBigInt(12345),
			TransactionIndex: fftypes.NewFFBigInt(10),
			BlockHash:        fftypes.NewRandB32().String(),
			ProtocolID:       fmt.Sprintf("%.12d/%.6d", fftypes.NewFFBigInt(12345).Int64(), fftypes.NewFFBigInt(10).Int64()),
			Success:          true,
			ContractLocation: fftypes.JSONAnyPtr(`{"address": "0x24746b95d118b2b4e8d07b06b1bad988fbf9415d"}`),
		})
		n.Transaction.Confirmed(context.Background(), []apitypes.BlockInfo{})
	}).Return(nil)
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(nil).Maybe()

	eh.WsServer = mws
	sth.toolkit.EventHandler = eh
	sth.inflight = []*pendingState{
		{
			confirmed: true,
			mtx: &apitypes.ManagedTX{
				ID:          fmt.Sprintf("ns1/%s", fftypes.NewUUID()),
				Created:     fftypes.Now(),
				SequenceID:  apitypes.NewULID().String(),
				Nonce:       fftypes.NewFFBigInt(1000),
				Status:      apitypes.TxStatusSucceeded,
				FirstSubmit: nil,
				Receipt:     &ffcapi.TransactionReceiptResponse{},
				TransactionHeaders: ffcapi.TransactionHeaders{
					From: "0x12345",
				},
			},
		},
	}

	h := txhistory.NewTxHistoryManager(sth.ctx)
	h.SetSubStatus(sth.ctx, sth.inflight[0].mtx, apitypes.TxSubStatusReceived)

	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("WriteTransaction", sth.ctx, mock.Anything, false).Return(fmt.Errorf("pop"))
	mp.On("Close", mock.Anything).Return(nil).Maybe()

	sth.policyLoopCycle(sth.ctx, false)

	mp.AssertExpectations(t)

}

func TestPolicyLoopUpdateEventHandlerError(t *testing.T) {

	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)

	txHash := "0x" + fftypes.NewRandB32().String()

	mfc := sth.toolkit.Connector.(*ffcapimocks.API)
	mfc.On("TransactionSend", sth.ctx, mock.MatchedBy(func(r *ffcapi.TransactionSendRequest) bool {
		return r.Nonce.Equals(fftypes.NewFFBigInt(1000))
	})).Return(&ffcapi.TransactionSendResponse{
		TransactionHash: txHash,
	}, ffcapi.ErrorReason(""), nil)

	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}
	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.MatchedBy(func(n *confirmations.Notification) bool {
		return n.NotificationType == confirmations.NewTransaction
	})).Run(func(args mock.Arguments) {
		n := args[0].(*confirmations.Notification)
		n.Transaction.Receipt(context.Background(), &ffcapi.TransactionReceiptResponse{
			BlockNumber:      fftypes.NewFFBigInt(12345),
			TransactionIndex: fftypes.NewFFBigInt(10),
			BlockHash:        fftypes.NewRandB32().String(),
			ProtocolID:       fmt.Sprintf("%.12d/%.6d", fftypes.NewFFBigInt(12345).Int64(), fftypes.NewFFBigInt(10).Int64()),
			Success:          true,
			ContractLocation: fftypes.JSONAnyPtr(`{"address": "0x24746b95d118b2b4e8d07b06b1bad988fbf9415d"}`),
		})
		n.Transaction.Confirmed(context.Background(), []apitypes.BlockInfo{})
	}).Return(nil)
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(fmt.Errorf("pop"))

	eh.WsServer = mws
	sth.toolkit.EventHandler = eh
	sth.inflight = []*pendingState{
		{
			confirmed: true,
			mtx: &apitypes.ManagedTX{
				ID:          fmt.Sprintf("ns1/%s", fftypes.NewUUID()),
				Created:     fftypes.Now(),
				SequenceID:  apitypes.NewULID().String(),
				Nonce:       fftypes.NewFFBigInt(1000),
				Status:      apitypes.TxStatusSucceeded,
				FirstSubmit: nil,
				Receipt:     &ffcapi.TransactionReceiptResponse{},
				TransactionHeaders: ffcapi.TransactionHeaders{
					From: "0x12345",
				},
			},
		},
	}

	h := txhistory.NewTxHistoryManager(sth.ctx)
	h.SetSubStatus(sth.ctx, sth.inflight[0].mtx, apitypes.TxSubStatusReceived)

	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("WriteTransaction", sth.ctx, mock.Anything, false).Return(nil)
	mp.On("Close", mock.Anything).Return(nil).Maybe()

	sth.policyLoopCycle(sth.ctx, false)

	mp.AssertExpectations(t)

}

func TestPolicyEngineFailStaleThenUpdated(t *testing.T) {
	f, tk, mockFFCAPI, conf, cleanup := newTestTransactionHandlerFactoryWithFilePersistence(t)
	defer cleanup()
	conf.SubSection(GasOracleConfig).Set(GasOracleMode, GasOracleModeConnector)
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	mmm := &metricsmocks.TransactionHandlerMetrics{}
	mmm.On("InitTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightUsed, metricsGaugeTransactionsInflightUsedDescription, false).Return(nil).Maybe()
	mmm.On("InitTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightFree, metricsGaugeTransactionsInflightFreeDescription, false).Return(nil).Maybe()
	mmm.On("InitTxHandlerCounterMetricWithLabels", mock.Anything, metricsCounterTransactionProcessOperationsTotal, metricsCounterTransactionProcessOperationsTotalDescription, []string{metricsLabelNameOperation}, true).Return(nil).Maybe()
	mmm.On("InitTxHandlerHistogramMetricWithLabels", mock.Anything, metricsHistogramTransactionProcessOperationsDuration, metricsHistogramTransactionProcessOperationsDurationDescription, []float64{}, []string{metricsLabelNameOperation}, true).Return(nil).Maybe()
	mmm.On("SetTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightUsed, mock.Anything, mock.Anything).Return().Maybe()
	mmm.On("SetTxHandlerGaugeMetric", mock.Anything, metricsGaugeTransactionsInflightFree, mock.Anything, mock.Anything).Return().Maybe()
	mmm.On("IncTxHandlerCounterMetricWithLabels", mock.Anything, metricsCounterTransactionProcessOperationsTotal, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	mmm.On("ObserveTxHandlerHistogramMetricWithLabels", mock.Anything, metricsHistogramTransactionProcessOperationsDuration, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()

	tk.MetricsManager = mmm
	sth := th.(*simpleTransactionHandler)

	ctx, cancelContext := context.WithCancel(context.Background())

	sth.Init(ctx, tk)

	done1 := make(chan struct{})
	mockFFCAPI.On("GasPriceEstimate", mock.Anything, mock.Anything).Return(nil, ffcapi.ErrorReason(""), fmt.Errorf("pop")).Once().
		Run(func(args mock.Arguments) {
			close(done1)
			sth.markInflightUpdate()
		})

	done2 := make(chan struct{})
	mockFFCAPI.On("GasPriceEstimate", mock.Anything, mock.Anything).Return(nil, ffcapi.ErrorReason(""), fmt.Errorf("pop")).Once().
		Run(func(args mock.Arguments) {
			close(done2)
		})
	_ = sendSampleTX(t, sth, "0xaaaaa", 12345)
	sth.policyLoopInterval = 1 * time.Hour
	policyLoopComplete, err := sth.Start(ctx)

	assert.Nil(t, err)
	<-done1

	<-done2
	cancelContext()

	<-policyLoopComplete
	mockFFCAPI.AssertExpectations(t)

}

func TestMarkInflightStaleDoesNotBlock(t *testing.T) {

	f, _, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.markInflightStale()
	sth.markInflightStale()

}

func TestMarkInflightUpdateDoesNotBlock(t *testing.T) {

	f, _, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.markInflightUpdate()
	sth.markInflightUpdate()

}

func TestExecPolicyGetTxFail(t *testing.T) {

	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)

	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)

	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("ListTransactionsByNonce", sth.ctx, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]*apitypes.ManagedTX{}, nil).Once()
	mp.On("WriteTransaction", sth.ctx, mock.Anything, mock.Anything).Return(nil, nil).Once()
	tx := sendSampleTX(t, sth, "0xaaaaa", 12345)
	mp.On("GetTransactionByID", sth.ctx, tx.ID).Return(nil, fmt.Errorf("pop"))

	req := &policyEngineAPIRequest{
		requestType: policyEngineAPIRequestTypeDelete,
		txID:        tx.ID,
		response:    make(chan policyEngineAPIResponse, 1),
	}
	sth.policyEngineAPIRequests = append(sth.policyEngineAPIRequests, req)

	sth.processPolicyAPIRequests(sth.ctx)

	res := <-req.response
	assert.Regexp(t, "pop", res.err)

	mp.AssertExpectations(t)

}
func TestExecPolicyDeleteFail(t *testing.T) {

	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)

	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)

	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("ListTransactionsByNonce", sth.ctx, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]*apitypes.ManagedTX{}, nil).Once()
	mp.On("WriteTransaction", sth.ctx, mock.Anything, mock.Anything).Return(nil, nil).Once()
	tx := sendSampleTX(t, sth, "0xaaaaa", 12345)
	mp.On("GetTransactionByID", sth.ctx, tx.ID).Return(tx, nil)
	mp.On("DeleteTransaction", sth.ctx, tx.ID).Return(fmt.Errorf("pop"))

	req := &policyEngineAPIRequest{
		requestType: policyEngineAPIRequestTypeDelete,
		txID:        tx.ID,
		response:    make(chan policyEngineAPIResponse, 1),
	}
	sth.policyEngineAPIRequests = append(sth.policyEngineAPIRequests, req)

	sth.processPolicyAPIRequests(sth.ctx)

	res := <-req.response
	assert.Regexp(t, "pop", res.err)

	mp.AssertExpectations(t)

}

func TestExecPolicyDeleteInflightSync(t *testing.T) {
	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)

	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)
	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}
	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.Anything).Return(nil)
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(nil).Maybe()

	eh.WsServer = mws
	sth.toolkit.EventHandler = eh
	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("ListTransactionsByNonce", sth.ctx, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]*apitypes.ManagedTX{}, nil).Once()
	mp.On("WriteTransaction", sth.ctx, mock.Anything, mock.Anything).Return(nil, nil).Once()
	tx := sendSampleTX(t, sth, "0xaaaaa", 12345)
	sth.inflight = []*pendingState{{mtx: tx}}
	mp.On("DeleteTransaction", sth.ctx, tx.ID).Return(nil)

	req := &policyEngineAPIRequest{
		requestType: policyEngineAPIRequestTypeDelete,
		txID:        tx.ID,
		response:    make(chan policyEngineAPIResponse, 1),
	}
	sth.policyEngineAPIRequests = append(sth.policyEngineAPIRequests, req)

	sth.processPolicyAPIRequests(sth.ctx)

	res := <-req.response
	assert.NoError(t, res.err)
	assert.Equal(t, http.StatusOK, res.status)
	assert.True(t, sth.inflight[0].remove)

	mp.AssertExpectations(t)

}

func TestExecPolicyIdempotentCancellation(t *testing.T) {
	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)

	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	ctx, cancelContext := context.WithCancel(context.Background())
	sth.ctx = ctx
	sth.Init(ctx, tk)
	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}
	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.Anything).Return(nil)
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(nil).Maybe()

	testTxID := fftypes.NewUUID()
	eh.WsServer = mws
	sth.toolkit.EventHandler = eh
	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("ListTransactionsPending", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]*apitypes.ManagedTX{}, nil)
	mp.On("DeleteTransaction", mock.Anything, testTxID.String()).Return(nil)
	sth.inflight = []*pendingState{{
		mtx: &apitypes.ManagedTX{
			ID: testTxID.String(),
		},
		remove: true,
	}}
	sth.ctx = nil
	txHandlerComplete, err := sth.Start(ctx)
	assert.Nil(t, err)

	mtx, err := sth.HandleCancelTransaction(sth.ctx, testTxID.String())
	cancelContext()
	<-txHandlerComplete
	assert.NoError(t, err)
	assert.Equal(t, testTxID.String(), mtx.ID)

	mp.AssertExpectations(t)

}

func TestPendingTransactionGetsRemoved(t *testing.T) {
	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)
	interval := 1 * time.Millisecond
	conf.Set(Interval, interval.String())
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	ctx, cancelContext := context.WithCancel(context.Background())
	sth.ctx = ctx
	sth.Init(ctx, tk)
	eh := &fftm.ManagedTransactionEventHandler{
		Ctx:       context.Background(),
		TxHandler: sth,
	}
	mc := &confirmationsmocks.Manager{}
	mc.On("Notify", mock.Anything).Return(nil)
	eh.ConfirmationManager = mc
	mws := &wsmocks.WebSocketServer{}
	mws.On("SendReply", mock.Anything).Return(nil).Maybe()

	testTxID := fftypes.NewUUID()
	eh.WsServer = mws
	sth.toolkit.EventHandler = eh
	deleteCalled := make(chan struct{})
	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("ListTransactionsPending", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]*apitypes.ManagedTX{}, nil)
	mp.On("DeleteTransaction", mock.Anything, testTxID.String()).Return(nil).Once().Run(func(args mock.Arguments) {
		close(deleteCalled)
	})
	mp.On("GetTransactionByID", mock.Anything, testTxID.String()).Return(&apitypes.ManagedTX{
		ID: testTxID.String(),
	}, nil)

	sth.ctx = nil
	txHandlerComplete, err := sth.Start(ctx)
	assert.Nil(t, err)

	// sleep to ensure inflightUpdate is drained
	time.Sleep(2 * interval)

	// add a delete request
	req := &policyEngineAPIRequest{
		requestType: policyEngineAPIRequestTypeDelete,
		txID:        testTxID.String(),
		response:    make(chan policyEngineAPIResponse, 1),
	}
	sth.policyEngineAPIRequests = append(sth.policyEngineAPIRequests, req)

	// wait for the next timed loop cycle to process the delete request
	<-deleteCalled

	cancelContext()
	<-txHandlerComplete

	mp.AssertExpectations(t)

}

func TestExecPolicyDeleteNotFound(t *testing.T) {

	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)

	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)
	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("ListTransactionsByNonce", sth.ctx, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]*apitypes.ManagedTX{}, nil).Once()
	mp.On("WriteTransaction", sth.ctx, mock.Anything, mock.Anything).Return(nil, nil).Once()
	tx := sendSampleTX(t, sth, "0xaaaaa", 12345)
	sth.inflight = []*pendingState{{mtx: tx}}
	mp.On("GetTransactionByID", sth.ctx, "bad-id").Return(nil, nil)

	req := &policyEngineAPIRequest{
		requestType: policyEngineAPIRequestTypeDelete,
		txID:        "bad-id",
		response:    make(chan policyEngineAPIResponse, 1),
	}
	sth.policyEngineAPIRequests = append(sth.policyEngineAPIRequests, req)

	sth.processPolicyAPIRequests(sth.ctx)

	res := <-req.response
	assert.Regexp(t, "FF21067", res.err)

	mp.AssertExpectations(t)

}

func TestBadTransactionAPIRequest(t *testing.T) {
	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)

	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)
	mp := sth.toolkit.TXPersistence.(*persistencemocks.TransactionPersistence)
	mp.On("ListTransactionsByNonce", sth.ctx, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]*apitypes.ManagedTX{}, nil).Once()
	mp.On("WriteTransaction", sth.ctx, mock.Anything, mock.Anything).Return(nil, nil).Once()
	tx := sendSampleTX(t, sth, "0xaaaaa", 12345)
	sth.inflight = []*pendingState{{mtx: tx}}

	req := &policyEngineAPIRequest{
		requestType: policyEngineAPIRequestType(999),
		txID:        tx.ID,
		response:    make(chan policyEngineAPIResponse, 1),
	}
	sth.policyEngineAPIRequests = append(sth.policyEngineAPIRequests, req)

	sth.processPolicyAPIRequests(sth.ctx)

	res := <-req.response
	assert.Regexp(t, "FF21073", res.err)

	mp.AssertExpectations(t)

}

func TestBadTransactionAPITimeout(t *testing.T) {

	f, _, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)

	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)

	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()

	res := sth.policyEngineAPIRequest(ctx, &policyEngineAPIRequest{})
	assert.Regexp(t, "FF21072", res.err)

}

func TestExecPolicyUpdateNewInfo(t *testing.T) {

	f, tk, _, conf := newTestTransactionHandlerFactory(t)
	conf.Set(FixedGasPrice, `12345`)
	conf.Set(ResubmitInterval, "100s")
	th, err := f.NewTransactionHandler(context.Background(), conf)
	assert.NoError(t, err)

	sth := th.(*simpleTransactionHandler)
	sth.ctx = context.Background()
	sth.Init(sth.ctx, tk)

	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()

	err = sth.execPolicy(ctx, &pendingState{
		mtx: &apitypes.ManagedTX{
			ID:          "id1",
			FirstSubmit: fftypes.Now(),
			Receipt:     &ffcapi.TransactionReceiptResponse{},
		},
	}, false)
	assert.NoError(t, err)

}
