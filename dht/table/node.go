package table

import (
	"net"
	"time"
)

// Node represents a single DHT node.
type Node struct {
	ID           NodeID
	Addr         *net.UDPAddr
	LastSeen     time.Time
	FailureCount int
}

// IsGood reports whether the node is considered good per BEP-05:
// responded within goodNodeWindow and has no consecutive failures.
func (n *Node) IsGood(goodNodeWindow time.Duration) bool {
	return time.Since(n.LastSeen) < goodNodeWindow && n.FailureCount == 0
}

// IsQuestionable reports whether the node has not been contacted within
// goodNodeWindow but has not exceeded badFailureThreshold.
func (n *Node) IsQuestionable(goodNodeWindow time.Duration, badFailureThreshold int) bool {
	return time.Since(n.LastSeen) >= goodNodeWindow && n.FailureCount < badFailureThreshold
}

// IsBad reports whether the node has failed badFailureThreshold or more consecutive queries.
func (n *Node) IsBad(badFailureThreshold int) bool {
	return n.FailureCount >= badFailureThreshold
}
