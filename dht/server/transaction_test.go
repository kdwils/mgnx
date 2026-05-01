package server

import (
	"net"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/krpc"
	"github.com/kdwils/mgnx/dht/table"
	"github.com/stretchr/testify/assert"
)

func testCfg() config.DHT {
	return config.DHT{
		BucketSize:          8,
		BadFailureThreshold: 2,
		StaleThreshold:      15 * time.Minute,
	}
}

func testTxnManager(t *testing.T, timeout time.Duration) (*TxnManager, *table.RoutingTable) {
	t.Helper()
	var serverNodeID table.NodeID
	cfg := testCfg()
	rt := table.NewRoutingTable(serverNodeID, cfg, nil)
	return NewTxnManager(rt, timeout), rt
}

func TestTransaction_deliver(t *testing.T) {
	t.Run("delivers message and closes channel", func(t *testing.T) {
		txn := &Transaction{Response: make(chan *krpc.Msg, 1)}
		msg := &krpc.Msg{T: "aa"}
		txn.deliver(msg)

		got, ok := <-txn.Response
		assert.True(t, ok)
		assert.Equal(t, msg, got)
	})

	t.Run("closes channel without value when msg is nil", func(t *testing.T) {
		txn := &Transaction{Response: make(chan *krpc.Msg, 1)}
		txn.deliver(nil)

		_, ok := <-txn.Response
		assert.False(t, ok)
	})

	t.Run("second deliver is a no-op", func(t *testing.T) {
		txn := &Transaction{Response: make(chan *krpc.Msg, 1)}
		msg := &krpc.Msg{T: "aa"}
		txn.deliver(msg)
		txn.deliver(msg) // must not panic or block
		assert.True(t, txn.done)
	})
}

func TestTxnManager_New(t *testing.T) {
	t.Run("returns transaction with 4-byte ID", func(t *testing.T) {
		m, _ := testTxnManager(t, 10*time.Second)
		addr := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 6881}
		txn := m.New(table.NodeID{}, addr)

		assert.Len(t, txn.ID, 4)
		assert.Equal(t, addr, txn.Addr)
		assert.NotNil(t, txn.Response)
	})

	t.Run("each transaction gets a unique ID", func(t *testing.T) {
		m, _ := testTxnManager(t, 10*time.Second)
		addr := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 6881}

		seen := make(map[string]struct{})
		for range 100 {
			txn := m.New(table.NodeID{}, addr)
			seen[txn.ID] = struct{}{}
			// keep it alive so IDs don't get reused
		}
		assert.Equal(t, 100, len(seen))
	})
}

func TestTxnManager_Complete(t *testing.T) {
	t.Run("delivers message to waiting goroutine", func(t *testing.T) {
		m, _ := testTxnManager(t, 10*time.Second)
		addr := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 6881}
		txn := m.New(table.NodeID{}, addr)

		msg := &krpc.Msg{T: txn.ID}
		m.Complete(txn.ID, msg)

		got, ok := <-txn.Response
		assert.True(t, ok)
		assert.Equal(t, msg, got)
	})

	t.Run("no-op for unknown ID", func(t *testing.T) {
		m, _ := testTxnManager(t, 10*time.Second)
		m.Complete("\x00\x01", &krpc.Msg{}) // must not panic
	})
}

func TestTxnKey(t *testing.T) {
	t.Run("encodes 0 as four zero bytes", func(t *testing.T) {
		assert.Equal(t, string([]byte{0x00, 0x00, 0x00, 0x00}), txnKey(0))
	})

	t.Run("encodes big-endian correctly", func(t *testing.T) {
		assert.Equal(t, string([]byte{0x00, 0x00, 0x01, 0x00}), txnKey(0x0100))
		assert.Equal(t, string([]byte{0x00, 0x00, 0xFF, 0xFF}), txnKey(0xFFFF))
		assert.Equal(t, string([]byte{0xFF, 0xFF, 0xFF, 0xFF}), txnKey(0xFFFFFFFF))
	})
}
