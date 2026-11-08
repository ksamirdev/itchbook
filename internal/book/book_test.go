package book

import (
	"errors"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/ksamirdev/itchbook/internal/itch"
)

// model is an obviously correct order book: maps, recomputed and sorted on
// every read. Books must agree with it after every message.
type model struct {
	orders map[uint64]order
	levels map[uint16]map[bool]map[uint32]*Level
}

func newModel() *model {
	return &model{orders: map[uint64]order{}, levels: map[uint16]map[bool]map[uint32]*Level{}}
}

func (m *model) level(o order) *Level {
	if m.levels[o.locate] == nil {
		m.levels[o.locate] = map[bool]map[uint32]*Level{false: {}, true: {}}
	}
	side := m.levels[o.locate][o.sell]
	if side[o.price] == nil {
		side[o.price] = &Level{Price: o.price}
	}
	return side[o.price]
}

func (m *model) take(ref uint64, shares uint32) {
	o := m.orders[ref]
	l := m.level(o)
	l.Shares -= uint64(shares)
	o.shares -= shares
	m.orders[ref] = o
	if o.shares == 0 {
		delete(m.orders, ref)
		if l.Orders--; l.Orders == 0 {
			delete(m.levels[o.locate][o.sell], o.price)
		}
	}
}

func (m *model) put(ref uint64, o order) {
	m.orders[ref] = o
	l := m.level(o)
	l.Shares += uint64(o.shares)
	l.Orders++
}

func (m *model) apply(msg *itch.Msg) error {
	o, live := m.orders[msg.Ref]
	switch msg.Type {
	case itch.AddOrder, itch.AddOrderMPID:
		if live {
			return ErrDuplicateRef
		}
		m.put(msg.Ref, order{price: msg.Price, shares: msg.Shares, locate: msg.Locate, sell: msg.Side == 'S'})
	case itch.Executed, itch.ExecutedPrice, itch.Cancel, itch.Delete:
		if !live {
			return ErrUnknownOrder
		}
		shares := msg.Shares
		if msg.Type == itch.Delete {
			shares = o.shares
		}
		if shares > o.shares {
			return ErrOverfill
		}
		m.take(msg.Ref, shares)
	case itch.Replace:
		if !live {
			return ErrUnknownOrder
		}
		if _, dup := m.orders[msg.NewRef]; dup {
			return ErrDuplicateRef
		}
		m.take(msg.Ref, o.shares)
		m.put(msg.NewRef, order{price: msg.Price, shares: msg.Shares, locate: o.locate, sell: o.sell})
	}
	return nil
}

func (m *model) depth(locate uint16, sell bool) []Level {
	var out []Level
	for _, l := range m.levels[locate][sell] {
		out = append(out, *l)
	}
	slices.SortFunc(out, func(a, b Level) int {
		if sell {
			return int(int64(a.Price) - int64(b.Price))
		}
		return int(int64(b.Price) - int64(a.Price))
	})
	return out
}

// gen produces a random stream of messages over a few stocks with prices
// clustered like a real book, plus some invalid ones that must be rejected.
type gen struct {
	r    *rand.Rand
	next uint64
	live map[uint64]order
}

func (g *gen) msg() itch.Msg {
	refs := slices.Sorted(maps.Keys(g.live))
	pick := func() (uint64, order) {
		if len(refs) == 0 || g.r.IntN(50) == 0 {
			return g.next + 1000, order{shares: 1} // unknown ref
		}
		ref := refs[g.r.IntN(len(refs))]
		return ref, g.live[ref]
	}
	price := func() uint32 { return uint32(10000 + g.r.IntN(40)*100) } // includes extreme low ask/high bid
	switch n := g.r.IntN(100); {
	case n < 45 || len(refs) == 0:
		g.next++
		ref := g.next
		if len(refs) > 0 && g.r.IntN(50) == 0 {
			ref = refs[0] // duplicate
		}
		side := byte('B')
		if g.r.IntN(2) == 0 {
			side = 'S'
		}
		return itch.Msg{Type: itch.AddOrder, Ref: ref, Locate: uint16(g.r.IntN(3)), Side: side,
			Shares: uint32(1 + g.r.IntN(500)), Price: price()}
	case n < 65:
		ref, o := pick()
		t := byte(itch.Executed)
		if g.r.IntN(2) == 0 {
			t = itch.Cancel
		}
		return itch.Msg{Type: t, Ref: ref, Shares: uint32(1 + g.r.IntN(int(o.shares)+1))} // sometimes overfills
	case n < 85:
		ref, _ := pick()
		return itch.Msg{Type: itch.Delete, Ref: ref}
	default:
		ref, _ := pick()
		g.next++
		return itch.Msg{Type: itch.Replace, Ref: ref, NewRef: g.next, Shares: uint32(1 + g.r.IntN(500)), Price: price()}
	}
}

func checkAgainstModel(t *testing.T, seed uint64, n int) {
	g := &gen{r: rand.New(rand.NewPCG(seed, 0)), live: map[uint64]order{}}
	bs, m := New(16), newModel() // tiny so the table grows and wraps
	for i := range n {
		msg := g.msg()
		// Round trip through the wire format so the decoder is covered too.
		msg, err := itch.Decode(itch.Append(nil, msg))
		if err != nil {
			t.Fatal(err)
		}
		got, want := bs.Apply(&msg), m.apply(&msg)
		if !errors.Is(got, want) {
			t.Fatalf("seed %d msg %d %+v: Apply returned %v, model %v", seed, i, msg, got, want)
		}
		g.live = m.orders
		if bs.Orders() != len(m.orders) {
			t.Fatalf("seed %d msg %d: %d orders, model %d", seed, i, bs.Orders(), len(m.orders))
		}
		for locate := range uint16(3) {
			for _, sell := range []bool{false, true} {
				got, want := bs.Book(locate).Depth(sell, 1000), m.depth(locate, sell)
				if !slices.Equal(got, want) {
					t.Fatalf("seed %d msg %d %+v: locate %d sell %v\n got %v\nwant %v", seed, i, msg, locate, sell, got, want)
				}
			}
		}
	}
}

func TestBooksMatchModel(t *testing.T) {
	for seed := range uint64(10) {
		checkAgainstModel(t, seed, 5000)
	}
}

func FuzzBooksMatchModel(f *testing.F) {
	f.Add(uint64(1))
	f.Fuzz(func(t *testing.T, seed uint64) { checkAgainstModel(t, seed, 2000) })
}

func TestApplyDoesNotAllocate(t *testing.T) {
	bs := New(16)
	msgs := []itch.Msg{
		{Type: itch.AddOrder, Ref: 1, Side: 'B', Shares: 100, Price: 100},
		{Type: itch.AddOrder, Ref: 2, Side: 'S', Shares: 100, Price: 101},
		{Type: itch.Executed, Ref: 1, Shares: 10},
		{Type: itch.Replace, Ref: 2, NewRef: 3, Shares: 50, Price: 102},
		{Type: itch.Delete, Ref: 1},
		{Type: itch.Delete, Ref: 3},
	}
	ref := uint64(1)
	allocs := testing.AllocsPerRun(1000, func() {
		ref += 3
		for _, m := range msgs {
			m.Ref += ref
			m.NewRef += ref
			if err := bs.Apply(&m); err != nil {
				t.Fatal(err)
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("Apply allocated %v times per run", allocs)
	}
}
