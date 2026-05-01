package crawler

import (
	"bytes"
	"time"
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
	return bytes.Compare(pq[i].dist[:], pq[j].dist[:]) < 0
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
