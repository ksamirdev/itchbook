package book

import (
	"math/bits"
	"unsafe"
)

// orders maps order reference to resting order. It replaces a Go map because
// the book needs to find an order once and then change or delete it in
// place, where a map costs a second hash and probe for every write.
//
// It is open addressing with linear probing, so a lookup usually touches one
// cache line, and backward-shift deletion, so deletes leave no tombstones to
// slow later lookups. See ADR 0003.
type orders struct {
	slots []slot
	shift uint // 64 - log2(len(slots))
	n     int
	grows int
}

type slot struct {
	ref  uint64
	used bool
	order
}

// newOrders sizes the table so capacity orders fit without growing.
func newOrders(capacity int) orders {
	size := 16
	for size < 2*capacity {
		size *= 2
	}
	t := orders{slots: make([]slot, size), shift: uint(64 - bits.TrailingZeros(uint(size)))}
	// Fresh memory from the OS is mapped on first write, one page fault at a
	// time. Take those faults now instead of in the middle of the feed.
	for i := 0; i < size; i += 4096 / int(unsafe.Sizeof(slot{})) {
		t.slots[i].used = false
	}
	return t
}

// home is the slot a ref hashes to. Nasdaq hands out refs to new orders
// nearly in order, so keeping each run of 512 consecutive refs in
// consecutive slots puts every add next to the previous add, in a cache line
// that is already loaded. Fibonacci hashing the run number scatters the runs
// so that long probe runs, which make deletes slow, cannot form. See ADR 0003.
func (t *orders) home(ref uint64) int {
	const run = 9 // log2 of the refs kept together
	block := (ref >> run) * 0x9E3779B97F4A7C15 >> (t.shift + run)
	return int((block<<run | ref&(1<<run-1)) & uint64(len(t.slots)-1))
}

// find returns the slot holding ref, or the empty slot where it would go.
func (t *orders) find(ref uint64) (int, bool) {
	mask := len(t.slots) - 1
	for i := t.home(ref); ; i = (i + 1) & mask {
		s := &t.slots[i]
		if !s.used {
			return i, false
		}
		if s.ref == ref {
			return i, true
		}
	}
}

// insert puts an order in the empty slot find returned for ref.
func (t *orders) insert(i int, ref uint64, o order) {
	t.slots[i] = slot{ref: ref, used: true, order: o}
	t.n++
	if t.n*2 > len(t.slots) {
		t.grow() // keeps probes short; stalls this one message, so size up front
	}
}

// remove empties slot i, then shifts later entries of the same probe run
// back so every lookup still finds them.
func (t *orders) remove(i int) {
	mask := len(t.slots) - 1
	for j := (i + 1) & mask; t.slots[j].used; j = (j + 1) & mask {
		// The entry at j can move to i unless its home lies cyclically in (i, j].
		k := t.home(t.slots[j].ref)
		if (i < j && (k <= i || k > j)) || (i > j && k <= i && k > j) {
			t.slots[i] = t.slots[j]
			i = j
		}
	}
	t.slots[i] = slot{}
	t.n--
}

func (t *orders) grow() {
	old, grows := t.slots, t.grows+1
	*t = newOrders(len(old))
	t.grows = grows
	for _, s := range old {
		if s.used {
			i, _ := t.find(s.ref)
			t.slots[i] = s
			t.n++
		}
	}
}
