package dht

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testTxnManager(t *testing.T, timeout time.Duration) (*TxnManager, *RoutingTable) {
	t.Helper()
	var ourID NodeID
	cfg := testCfg()
	rt := NewRoutingTable(ourID, cfg, nil)
	return NewTxnManager(rt, timeout), rt
}

func TestTransaction_deliver(t *testing.T) {
	t.Run("delivers message and closes channel", func(t *testing.T) {
		txn := &Transaction{Response: make(chan *Msg, 1)}
		msg := &Msg{T: "aa"}
		txn.deliver(msg)

		got, ok := <-txn.Response
		assert.True(t, ok)
		assert.Equal(t, msg, got)
	})

	t.Run("closes channel without value when msg is nil", func(t *testing.T) {
		txn := &Transaction{Response: make(chan *Msg, 1)}
		txn.deliver(nil)

		_, ok := <-txn.Response
		assert.False(t, ok)
	})

	t.Run("second deliver is a no-op", func(t *testing.T) {
		txn := &Transaction{Response: make(chan *Msg, 1)}
		msg := &Msg{T: "aa"}
		txn.deliver(msg)
		txn.deliver(msg) // must not panic or block
		assert.True(t, txn.done)
	})
}

func TestTxnManager_New(t *testing.T) {
	t.Run("returns transaction with 2-byte ID", func(t *testing.T) {
		m, _ := testTxnManager(t, 10*time.Second)
		addr := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 6881}
		txn := m.New(NodeID{}, addr)

		assert.Len(t, txn.ID, 2)
		assert.Equal(t, addr, txn.Addr)
		assert.NotNil(t, txn.Response)
	})

	t.Run("each transaction gets a unique ID", func(t *testing.T) {
		m, _ := testTxnManager(t, 10*time.Second)
		addr := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 6881}

		seen := make(map[string]struct{})
		for range 100 {
			txn := m.New(NodeID{}, addr)
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
		txn := m.New(NodeID{}, addr)

		msg := &Msg{T: txn.ID}
		m.Complete(txn.ID, msg)

		got, ok := <-txn.Response
		assert.True(t, ok)
		assert.Equal(t, msg, got)
	})

	t.Run("no-op for unknown ID", func(t *testing.T) {
		m, _ := testTxnManager(t, 10*time.Second)
		m.Complete("\x00\x01", &Msg{}) // must not panic
	})
}

func TestTxnManager_sweeper(t *testing.T) {
	t.Run("times out transaction and marks node failure", func(t *testing.T) {
		timeout := 50 * time.Millisecond
		m, rt := testTxnManager(t, timeout)

		// Insert a node so MarkFailure has something to act on.
		node := makeNode(0x10, 1000)
		rt.Insert(node)

		ctx := t.Context()
		m.Start(ctx)

		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1000}
		txn := m.New(node.ID, addr)

		// Wait for the Response channel to be closed by the sweeper.
		select {
		case _, ok := <-txn.Response:
			require.False(t, ok, "expected channel closed on timeout")
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for sweeper")
		}

		// Node failure count should have been incremented.
		b := rt.buckets[rt.bucketFor(node.ID)]
		var found *Node
		for _, n := range b.Nodes {
			if n.ID == node.ID {
				found = n
				break
			}
		}
		require.NotNil(t, found)
		assert.Equal(t, 1, found.FailureCount)
	})
}

func TestTxnKey(t *testing.T) {
	t.Run("encodes 0 as two zero bytes", func(t *testing.T) {
		assert.Equal(t, string([]byte{0x00, 0x00}), txnKey(0))
	})

	t.Run("encodes big-endian correctly", func(t *testing.T) {
		assert.Equal(t, string([]byte{0x01, 0x00}), txnKey(0x0100))
		assert.Equal(t, string([]byte{0xFF, 0xFF}), txnKey(0xFFFF))
	})
}
