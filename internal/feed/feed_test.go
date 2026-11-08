package feed

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/ksamirdev/itchbook/internal/book"
	"github.com/ksamirdev/itchbook/internal/exchange"
	"github.com/ksamirdev/itchbook/internal/itch"
	"github.com/ksamirdev/itchbook/internal/mold"
)

const stocks = 4

// session builds n messages that add orders and delete about half of them,
// so applying any message twice or out of order breaks the book.
func session(r *rand.Rand, n int) *exchange.Session {
	s := exchange.NewSession([10]byte{'T', 'E', 'S', 'T'})
	for locate := range uint16(stocks) {
		s.Add(itch.Append(nil, itch.Msg{Type: itch.StockDirectory, Locate: locate}))
	}
	var live []uint64
	for ref := uint64(1); s.Len() < uint64(n); {
		if len(live) > 0 && r.IntN(2) == 0 {
			i := r.IntN(len(live))
			s.Add(itch.Append(nil, itch.Msg{Type: itch.Delete, Ref: live[i]}))
			live = slices.Delete(live, i, i+1)
			continue
		}
		side := byte('B')
		if r.IntN(2) == 0 {
			side = 'S'
		}
		s.Add(itch.Append(nil, itch.Msg{Type: itch.AddOrder, Ref: ref, Side: side, Locate: uint16(r.IntN(stocks)),
			Shares: uint32(1 + r.IntN(100)), Price: uint32(100 + r.IntN(20))}))
		live = append(live, ref)
		ref++
	}
	return s
}

func reference(s *exchange.Session) *book.Books {
	bs := book.New(16)
	for seq := uint64(1); seq <= s.Len(); seq++ {
		m, _ := itch.Decode(s.Msg(seq))
		if err := bs.Apply(&m); err != nil {
			panic(err)
		}
	}
	return bs
}

func sameBooks(t *testing.T, got, want *book.Books) {
	t.Helper()
	if got.Orders() != want.Orders() {
		t.Fatalf("%d resting orders, want %d", got.Orders(), want.Orders())
	}
	for locate := range uint16(stocks) {
		for _, sell := range []bool{false, true} {
			g, w := got.Book(locate).Depth(sell, 1000), want.Book(locate).Depth(sell, 1000)
			if !slices.Equal(g, w) {
				t.Fatalf("locate %d sell %v:\n got %v\nwant %v", locate, sell, g, w)
			}
		}
	}
}

// TestLossyLines sends every packet on two lines that each drop, duplicate
// and reorder packets, answers re-requests with the same faults, and checks
// the books end up exactly as if every message arrived once in order.
func TestLossyLines(t *testing.T) {
	for seed := range uint64(30) {
		r := rand.New(rand.NewPCG(seed, 0))
		s := session(r, 3000)
		h := New(book.New(16))
		h.RetryAfter = 50 * time.Microsecond // each delivery below takes 1µs

		var queue [][]byte
		send := func(pkt []byte) {
			for range 1 + r.IntN(2) { // sometimes delivered twice
				if r.Float64() < 0.2 {
					continue // lost
				}
				i := len(queue) - r.IntN(min(len(queue), 2)+1) // sometimes overtakes a packet or two
				queue = slices.Insert(queue, i, slices.Clone(pkt))
			}
		}
		var p mold.Packer
		for seq := uint64(1); seq <= s.Len(); {
			next := s.Pack(&p, seq, uint64(1+r.IntN(20)))
			send(p.Packet()) // line A
			send(p.Packet()) // line B
			seq = next
		}
		for range 5 {
			send(s.EndOfSession())
		}

		for step := 0; !h.Done(); step++ {
			if step > 1_000_000 {
				t.Fatalf("seed %d: stuck at seq %d of %d, %+v", seed, h.Next(), s.Len(), h.Stats)
			}
			if len(queue) == 0 {
				send(s.EndOfSession()) // a real exchange keeps sending heartbeats
				continue
			}
			pkt := queue[0]
			queue = queue[1:]
			req, err := h.Packet(pkt, time.Unix(0, int64(step)*1000))
			if err != nil {
				t.Fatal(err)
			}
			if req != nil {
				rh, _ := mold.ParseHeader(req)
				for seq, end := rh.Seq, rh.Seq+uint64(rh.Count); seq < end; {
					next := s.Pack(&p, seq, end-seq)
					send(p.Packet())
					seq = next
				}
			}
		}
		if h.Stats.Messages != s.Len() || h.Stats.BadMsgs != 0 {
			t.Fatalf("seed %d: %+v", seed, h.Stats)
		}
		sameBooks(t, h.Books, reference(s))
	}
}

func TestRejectsOtherSession(t *testing.T) {
	s := session(rand.New(rand.NewPCG(1, 1)), 10)
	h := New(book.New(16))
	var p mold.Packer
	s.Pack(&p, 1, 1)
	if _, err := h.Packet(p.Packet(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	p.Session = [10]byte{'O', 'T', 'H', 'E', 'R'}
	p.Start(2)
	if _, err := h.Packet(p.Packet(), time.Time{}); !errors.Is(err, ErrWrongSession) {
		t.Fatalf("got %v", err)
	}
}

func TestPacketDoesNotAllocate(t *testing.T) {
	s := session(rand.New(rand.NewPCG(2, 2)), 100_000)
	h := New(book.New(1 << 17))
	var pkts [][]byte
	var p mold.Packer
	for seq := uint64(1); seq <= s.Len(); {
		seq = s.Pack(&p, seq, 1<<16)
		pkts = append(pkts, slices.Clone(p.Packet()))
	}
	i := 0
	allocs := testing.AllocsPerRun(len(pkts)-1, func() {
		if _, err := h.Packet(pkts[i], time.Time{}); err != nil {
			t.Fatal(err)
		}
		i++
	})
	if allocs != 0 {
		t.Fatalf("%v allocations per packet", allocs)
	}
}

// TestOneRequestPerGap loses a long stretch of packets on both lines and
// delivers each answer packet by packet. The handler must not ask again for
// a range that is still arriving: one request for the gap, and one for the
// packets it dropped while the gap was open.
func TestOneRequestPerGap(t *testing.T) {
	s := session(rand.New(rand.NewPCG(3, 3)), 5000)
	h := New(book.New(1 << 12))
	var p mold.Packer
	var pkts [][]byte
	for seq := uint64(1); seq <= s.Len(); {
		seq = s.Pack(&p, seq, 10)
		pkts = append(pkts, slices.Clone(p.Packet()))
	}
	now := time.Unix(0, 0)
	var asks []mold.Header
	deliver := func(pkt []byte) {
		if req, _ := h.Packet(pkt, now); req != nil {
			rh, _ := mold.ParseHeader(req)
			asks = append(asks, rh)
		}
	}
	for i, pkt := range pkts {
		if i < 10 || i >= 400 { // 10..399 lost on both lines
			deliver(pkt)
		}
	}
	for answered := 0; answered < len(asks); answered++ {
		rh := asks[answered]
		for seq, end := rh.Seq, rh.Seq+uint64(rh.Count); seq < end; {
			seq = s.Pack(&p, seq, end-seq)
			deliver(p.Packet())
		}
	}
	if len(asks) != 2 || asks[1].Seq != asks[0].Seq+uint64(asks[0].Count) || h.Next() != s.Len()+1 {
		t.Fatalf("re-requests %+v, next %d of %d", asks, h.Next(), s.Len()+1)
	}

	// Lose the end of the session and its answer: retries back off.
	h2 := New(book.New(1 << 12))
	for _, pkt := range pkts[:5] {
		_, _ = h2.Packet(pkt, now)
	}
	eos := s.EndOfSession()
	var sentAt []time.Duration
	for ms := range 100 {
		at := time.Duration(ms) * time.Millisecond
		if req, _ := h2.Packet(eos, now.Add(at)); req != nil {
			sentAt = append(sentAt, at)
		}
	}
	want := []time.Duration{0, 2, 6, 14, 30, 62} // RetryAfter doubling from 2ms
	for i := range want {
		want[i] *= time.Millisecond
	}
	if !slices.Equal(sentAt, want) {
		t.Fatalf("re-requests at %v, want %v", sentAt, want)
	}
}
