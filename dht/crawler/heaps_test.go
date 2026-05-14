package crawler

import (
	"testing"

	"github.com/kdwils/mgnx/dht/table"
)

func TestScaleDist(t *testing.T) {
	tests := []struct {
		name   string
		dist   table.NodeID
		factor float64
		expect table.NodeID
	}{
		{
			name:   "zero distance stays zero",
			dist:   table.NodeID{},
			factor: 0.5,
			expect: table.NodeID{},
		},
		{
			name:   "factor 1.0 preserves distance",
			dist:   table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255},
			factor: 1.0,
			expect: table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255},
		},
		{
			name:   "factor 0.5 halves distance",
			dist:   table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 200},
			factor: 0.5,
			expect: table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 100},
		},
		{
			name:   "factor 0 produces zero",
			dist:   table.NodeID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
			factor: 0.0,
			expect: table.NodeID{},
		},
		{
			name:   "multi-byte carry propagates",
			dist:   table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0},
			factor: 0.5,
			expect: table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scaleDist(tt.dist, tt.factor)
			if got != tt.expect {
				t.Errorf("scaleDist() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestCmpDist(t *testing.T) {
	tests := []struct {
		name string
		a    table.NodeID
		b    table.NodeID
		want int
	}{
		{
			name: "equal distances return 0",
			a:    table.NodeID{1, 2, 3},
			b:    table.NodeID{1, 2, 3},
			want: 0,
		},
		{
			name: "a less than b returns -1",
			a:    table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
			b:    table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2},
			want: -1,
		},
		{
			name: "a greater than b returns 1",
			a:    table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0},
			b:    table.NodeID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255},
			want: 1,
		},
		{
			name: "zero vs non-zero",
			a:    table.NodeID{},
			b:    table.NodeID{255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255},
			want: -1,
		},
		{
			name: "difference at first byte",
			a:    table.NodeID{10, 0, 0},
			b:    table.NodeID{20, 0, 0},
			want: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cmpDist(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("cmpDist(%v, %v) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestTraversalHeap_Less_WithYield(t *testing.T) {
	nodeA := &table.Node{ID: table.NodeID{1}}
	nodeB := &table.Node{ID: table.NodeID{2}}
	target := table.NodeID{}

	distA := target.XOR(nodeA.ID)
	distB := target.XOR(nodeB.ID)

	itemA := &traversalItem{node: nodeA, target: target, dist: distA, yieldFactor: 0}
	itemB := &traversalItem{node: nodeB, target: target, dist: distB, yieldFactor: 0}

	h := traversalHeap{itemA, itemB}

	if !h.Less(0, 1) {
		t.Error("expected itemA (closer) to be less than itemB when both have zero yield")
	}

	itemZeroA := &traversalItem{node: nodeA, target: target, dist: distA, yieldFactor: 0}
	itemZeroB := &traversalItem{node: nodeB, target: target, dist: distA, yieldFactor: 0}
	h2 := traversalHeap{itemZeroA, itemZeroB}

	if h2.Less(0, 1) || h2.Less(1, 0) {
		t.Error("items with identical distance and zero yield should be equal")
	}

	itemNoYield := &traversalItem{node: nodeA, target: target, dist: distA, yieldFactor: 0}
	itemHighYield := &traversalItem{node: nodeB, target: target, dist: distA, yieldFactor: 100}
	h3 := traversalHeap{itemNoYield, itemHighYield}

	if !h3.Less(1, 0) {
		t.Error("item with positive yield should be preferred over identical distance with no yield")
	}
}
