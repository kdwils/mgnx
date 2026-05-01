package server

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kdwils/mgnx/dht/krpc"
	"github.com/kdwils/mgnx/dht/table"
	"github.com/kdwils/mgnx/pkg/cache"
)

// Transaction represents a single in-flight KRPC query.
// Response is closed exactly once — either when a reply arrives or on timeout.
// Callers wait with: resp, ok := <-txn.Response (ok=false means timeout).
type Transaction struct {
	ID       string
	NodeID   table.NodeID // queried node; used to call MarkFailure on timeout
	Addr     *net.UDPAddr
	SentAt   time.Time
	Response chan *krpc.Msg // buffered capacity 1; closed on delivery or timeout
	mu       sync.Mutex
	done     bool
}

// deliver sends msg and closes the Response channel exactly once.
// If msg is nil the channel is closed without a value, signalling a timeout.
// Concurrent calls after the first are no-ops.
func (t *Transaction) deliver(msg *krpc.Msg) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	if msg != nil {
		t.Response <- msg
	}
	close(t.Response)
}

// TxnManager owns the in-flight transaction map and the timeout sweeper.
type TxnManager struct {
	cache   *cache.Cache[string, *Transaction]
	counter atomic.Uint32
	table   *table.RoutingTable
	timeout time.Duration
}

// NewTxnManager creates a TxnManager. Call Start to launch the sweeper.
func NewTxnManager(table *table.RoutingTable, timeout time.Duration) *TxnManager {
	m := &TxnManager{table: table, timeout: timeout}
	m.cache = cache.New(
		cache.WithCleanup(time.Second, func(_ string, txn *Transaction) bool {
			if txn.done {
				return true
			}
			if time.Since(txn.SentAt) <= timeout {
				return false
			}
			// Mark failure before closing the channel so callers that
			// unblock on the close observe the updated routing table state.
			if table != nil {
				table.MarkFailure(txn.NodeID)
			}
			txn.deliver(nil)
			return true
		}),
	)
	return m
}

// Start launches the background sweeper. It runs until ctx is cancelled.
func (m *TxnManager) Start(ctx context.Context) {
	m.cache.StartCleanup(ctx)
}

// New allocates a Transaction with a unique 4-byte ID, registers it in the
// map, and returns it. The caller is responsible for sending the query.
func (m *TxnManager) New(nodeID table.NodeID, addr *net.UDPAddr) *Transaction {
	for {
		raw := m.counter.Add(1)
		key := txnKey(raw)

		// skip nodes that are currently in-flight
		if _, ok := m.cache.Get(key); ok {
			continue
		}

		txn := &Transaction{
			ID:       key,
			NodeID:   nodeID,
			Addr:     addr,
			SentAt:   time.Now(),
			Response: make(chan *krpc.Msg, 1),
		}
		m.cache.Set(key, txn)
		return txn
	}
}

// Complete delivers msg to the waiting caller for the transaction with the
// given ID. It is a no-op if the transaction has already been completed or
// timed out.
func (m *TxnManager) Complete(id string, msg *krpc.Msg) {
	txn, ok := m.cache.Get(id)
	if !ok {
		return
	}
	txn.deliver(msg)
}

// txnKey encodes a 32-bit counter as a raw 4-byte big-endian binary string.
// 4 bytes (2^32 possible IDs) makes off-path response injection via ID
// brute-force infeasible compared to the previous 2-byte scheme.
func txnKey(id uint32) string {
	return string([]byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)})
}
