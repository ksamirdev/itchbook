package book

import (
	"math/rand/v2"
	"testing"
)

// TestOrdersMatchMap inserts and removes refs shaped like Nasdaq's (mostly
// rising, some far apart) in tables small enough to wrap and grow, and
// checks every ref is still found where a map says it should be.
func TestOrdersMatchMap(t *testing.T) {
	for _, capacity := range []int{8, 600, 5000} {
		r := rand.New(rand.NewPCG(uint64(capacity), 0))
		tab, want := newOrders(capacity), map[uint64]uint32{}
		var live []uint64
		next := uint64(1)
		for step := range 200_000 {
			if len(live) > 0 && r.IntN(100) < 48 {
				i := r.IntN(len(live))
				ref := live[i]
				live[i] = live[len(live)-1]
				live = live[:len(live)-1]
				slot, ok := tab.find(ref)
				if !ok || tab.slots[slot].shares != want[ref] {
					t.Fatalf("capacity %d step %d: ref %d not found", capacity, step, ref)
				}
				tab.remove(slot)
				delete(want, ref)
			} else {
				next += 1 + uint64(r.IntN(3))
				if r.IntN(20) == 0 {
					next += uint64(r.IntN(1 << 20)) // a jump, so runs collide
				}
				slot, ok := tab.find(next)
				if ok {
					t.Fatalf("ref %d found before insert", next)
				}
				tab.insert(slot, next, order{shares: uint32(step)})
				want[next] = uint32(step)
				live = append(live, next)
			}
			if tab.n != len(want) {
				t.Fatalf("table has %d, map %d", tab.n, len(want))
			}
		}
		for ref, shares := range want {
			if slot, ok := tab.find(ref); !ok || tab.slots[slot].shares != shares {
				t.Fatalf("capacity %d: ref %d lost", capacity, ref)
			}
		}
	}
}
