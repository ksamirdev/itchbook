// Package book rebuilds every stock's order book from ITCH messages.
//
// The hot path never allocates once its slices have grown, and nothing it
// keeps holds a pointer, so the garbage collector has nothing to scan or free.
package book

import (
	"errors"
	"slices"

	"github.com/ksamirdev/itchbook/internal/itch"
)

// Level is one price on one side of a book.
type Level struct {
	Price  uint32 // 4 implied decimal places
	Shares uint64
	Orders uint32
}

// level is stored by key so both sides sort the same way with the best price
// last: bids keep the price, asks keep its complement.
type level struct {
	key    uint32
	orders uint32
	shares uint64
}

// side is a book side sorted by key, best price last, because most updates
// land near the top of the book and inserting there moves the fewest levels.
type side []level

// find returns the index of key, or where to insert it. A linear scan from
// the best price measured slower at p99 on real data; see ADR 0002.
func (s side) find(key uint32) (int, bool) {
	lo, hi := 0, len(s)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if s[m].key < key {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo, lo < len(s) && s[lo].key == key
}

func (s *side) add(key, shares uint32) {
	i, ok := s.find(key)
	if ok {
		(*s)[i].orders++
		(*s)[i].shares += uint64(shares)
		return
	}
	*s = slices.Insert(*s, i, level{key: key, orders: 1, shares: uint64(shares)})
}

// reduce takes shares off the level at key, and one order when gone is true.
func (s *side) reduce(key, shares uint32, gone bool) {
	i, ok := s.find(key)
	if !ok {
		panic("book: order's level is missing") // the order table and levels disagree
	}
	l := &(*s)[i]
	l.shares -= uint64(shares)
	if gone {
		l.orders--
		if l.orders == 0 {
			*s = slices.Delete(*s, i, i+1)
		}
	}
}

// reservedLevels is reserved per side when a stock is announced. A side
// that outgrows it doubles, which allocates in the middle of a message.
const reservedLevels = 64

// Book is one stock's order book.
type Book struct {
	bids, asks side
	state      byte // trading state from the last Trading Action message
}

// Trading reports whether the stock is in continuous trading. A halted or
// paused stock still accepts orders without matching them, so its book can
// legitimately be locked or crossed until it reopens.
func (b *Book) Trading() bool { return b.state == 'T' }

func (b *Book) side(sell bool) *side {
	if sell {
		return &b.asks
	}
	return &b.bids
}

func key(price uint32, sell bool) uint32 {
	if sell {
		return ^price
	}
	return price
}

// Best returns the best bid or ask.
func (b *Book) Best(sell bool) (Level, bool) {
	s := *b.side(sell)
	if len(s) == 0 {
		return Level{}, false
	}
	l := s[len(s)-1]
	return Level{Price: key(l.key, sell), Shares: l.shares, Orders: l.orders}, true
}

// Depth returns up to n levels of one side, best first.
func (b *Book) Depth(sell bool, n int) []Level {
	s := *b.side(sell)
	out := make([]Level, 0, min(n, len(s)))
	for i := len(s) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, Level{Price: key(s[i].key, sell), Shares: s[i].shares, Orders: s[i].orders})
	}
	return out
}

// order is a resting order. Refs are unique across all stocks for a day.
type order struct {
	price  uint32
	shares uint32
	locate uint16
	sell   bool
}

var (
	ErrUnknownOrder = errors.New("book: message references an order that is not on the book")
	ErrDuplicateRef = errors.New("book: add reuses a live order reference")
	ErrOverfill     = errors.New("book: execute or cancel exceeds the order's open shares")
)

// Books holds the book of every stock on the feed, indexed by stock locate.
type Books struct {
	books  [1 << 16]Book
	names  [1 << 16][8]byte
	orders orders
}

// New sizes the order table for capacity resting orders. Going past it is
// fine but grows the table, which stalls that one message.
func New(capacity int) *Books {
	return &Books{orders: newOrders(capacity)}
}

// Book returns the book for a stock locate.
func (bs *Books) Book(locate uint16) *Book { return &bs.books[locate] }

// Name returns the symbol for a stock locate, space padded.
func (bs *Books) Name(locate uint16) [8]byte { return bs.names[locate] }

// Orders returns how many orders are resting across all books.
func (bs *Books) Orders() int { return bs.orders.n }

// Grows returns how many times the order table outgrew its capacity.
func (bs *Books) Grows() int { return bs.orders.grows }

// Apply updates the books with one message. On error the books are unchanged.
func (bs *Books) Apply(m *itch.Msg) error {
	switch m.Type {
	case itch.AddOrder, itch.AddOrderMPID:
		return bs.add(m.Ref, order{price: m.Price, shares: m.Shares, locate: m.Locate, sell: m.Side == 'S'})
	case itch.Executed, itch.ExecutedPrice, itch.Cancel:
		return bs.reduce(m.Ref, m.Shares, false)
	case itch.Delete:
		return bs.reduce(m.Ref, 0, true)
	case itch.Replace:
		i, ok := bs.orders.find(m.Ref)
		if !ok {
			return ErrUnknownOrder
		}
		if _, dup := bs.orders.find(m.NewRef); dup {
			return ErrDuplicateRef
		}
		o := bs.orders.slots[i].order
		bs.remove(i)
		// The replacement keeps the side and stock and loses time priority.
		return bs.add(m.NewRef, order{price: m.Price, shares: m.Shares, locate: o.locate, sell: o.sell})
	case itch.StockDirectory:
		// Stocks are announced before the open, so reserving levels here
		// keeps the first orders of the day from allocating.
		b := &bs.books[m.Locate]
		b.bids, b.asks = make(side, 0, reservedLevels), make(side, 0, reservedLevels)
		bs.names[m.Locate] = m.Stock
	case itch.TradingAction:
		bs.books[m.Locate].state = m.State
	}
	return nil
}

func (bs *Books) add(ref uint64, o order) error {
	i, dup := bs.orders.find(ref)
	if dup {
		return ErrDuplicateRef
	}
	bs.books[o.locate].side(o.sell).add(key(o.price, o.sell), o.shares)
	bs.orders.insert(i, ref, o)
	return nil
}

// reduce takes shares off an order, or all of them when all is true.
func (bs *Books) reduce(ref uint64, shares uint32, all bool) error {
	i, ok := bs.orders.find(ref)
	if !ok {
		return ErrUnknownOrder
	}
	o := &bs.orders.slots[i].order
	if all || shares == o.shares {
		bs.remove(i)
		return nil
	}
	if shares > o.shares {
		return ErrOverfill
	}
	o.shares -= shares
	bs.books[o.locate].side(o.sell).reduce(key(o.price, o.sell), shares, false)
	return nil
}

func (bs *Books) remove(i int) {
	o := bs.orders.slots[i].order
	bs.books[o.locate].side(o.sell).reduce(key(o.price, o.sell), o.shares, true)
	bs.orders.remove(i)
}
