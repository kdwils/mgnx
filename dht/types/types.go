package types

import (
	"net"
	"time"
)

// PeerAddr represents a TCP peer address for metadata fetching.
type PeerAddr struct {
	SourceIP net.IP
	Port     int
}

// DiscoveredPeers is the coupling point between the DHT layer and the metadata fetch layer.
type DiscoveredPeers struct {
	Infohash [20]byte
	Peers    []PeerAddr
	SeenAt   time.Time
}
