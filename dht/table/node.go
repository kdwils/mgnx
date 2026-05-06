package table

import (
	"encoding/json"
	"net"
	"time"
)

// Node represents a single DHT node.
type Node struct {
	ID           NodeID       `json:"id"`
	Addr         *net.UDPAddr `json:"addr"`
	LastSeen     time.Time    `json:"last_seen"`
	FailureCount int          `json:"failure_count"`
}

type nodeJSON struct {
	ID           NodeID    `json:"id"`
	Addr         string    `json:"addr"`
	LastSeen     time.Time `json:"last_seen"`
	FailureCount int       `json:"failure_count"`
}

func (n *Node) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeJSON{
		ID:           n.ID,
		Addr:         n.Addr.String(),
		LastSeen:     n.LastSeen,
		FailureCount: n.FailureCount,
	})
}

func (n *Node) UnmarshalJSON(data []byte) error {
	var nj nodeJSON
	if err := json.Unmarshal(data, &nj); err != nil {
		return err
	}
	addr, err := net.ResolveUDPAddr("udp", nj.Addr)
	if err != nil {
		return err
	}
	n.ID = nj.ID
	n.Addr = addr
	n.LastSeen = nj.LastSeen
	n.FailureCount = nj.FailureCount
	return nil
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
