package crawler

import (
	"time"

	"github.com/kdwils/mgnx/dht/table"
)

// cooldownItem holds a traversalItem waiting for its cooldown to expire.
type cooldownItem struct {
	item        *traversalItem
	nextAllowed time.Time
}

// cooldownHeap is a min-heap of *cooldownItem ordered by nextAllowed time.
type cooldownHeap []*cooldownItem

func (h cooldownHeap) Len() int           { return len(h) }
func (h cooldownHeap) Less(i, j int) bool { return h[i].nextAllowed.Before(h[j].nextAllowed) }
func (h cooldownHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *cooldownHeap) Push(x any)        { *h = append(*h, x.(*cooldownItem)) }
func (h *cooldownHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// traversalHeap is a min-heap of *traversalItem ordered by XOR distance to target.
type traversalHeap []*traversalItem

func (pq traversalHeap) Len() int { return len(pq) }
func (pq traversalHeap) Less(i, j int) bool {
	di, dj := pq[i].dist, pq[j].dist

	// Apply yield-based discount: nodes with higher sample yield get
	// a distance reduction, biasing the crawler toward productive regions.
	if pq[i].yieldFactor > 0 {
		discount := pq[i].yieldFactor / (pq[i].yieldFactor + 1.0)
		di = scaleDist(di, discount)
	}
	if pq[j].yieldFactor > 0 {
		discount := pq[j].yieldFactor / (pq[j].yieldFactor + 1.0)
		dj = scaleDist(dj, discount)
	}

	return cmpDist(di, dj) < 0
}

// scaleDist multiplies a 20-byte XOR distance by a discount factor (0 < f < 1).
func scaleDist(dist table.NodeID, factor float64) table.NodeID {
	var result table.NodeID
	carry := 0.0
	for k := len(dist) - 1; k >= 0; k-- {
		v := float64(dist[k])*factor + carry
		result[k] = byte(v)
		carry = v - float64(result[k])
	}
	return result
}

func cmpDist(a, b table.NodeID) int {
	for k := range a {
		if a[k] < b[k] {
			return -1
		}
		if a[k] > b[k] {
			return 1
		}
	}
	return 0
}

func (pq traversalHeap) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *traversalHeap) Push(x any) {
	item := x.(*traversalItem)
	item.index = len(*pq)
	*pq = append(*pq, item)
}

func (pq *traversalHeap) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*pq = old[:n-1]
	item.index = -1
	return item
}
