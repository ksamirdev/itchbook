// Package itch decodes the Nasdaq TotalView-ITCH 5.0 messages an order book
// needs. Spec: https://www.nasdaqtrader.com/content/technicalsupport/specifications/dataproducts/NQTVITCHspecification.pdf
//
// Decoding reads fixed big-endian offsets straight out of the input slice,
// never allocates, and keeps nothing that points into the input.
package itch

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// Message types the order book uses. Every other type decodes to just its
// header and is ignored by the book.
const (
	SystemEvent    = 'S'
	StockDirectory = 'R'
	TradingAction  = 'H'
	AddOrder       = 'A'
	AddOrderMPID   = 'F'
	Executed       = 'E'
	ExecutedPrice  = 'C'
	Cancel         = 'X'
	Delete         = 'D'
	Replace        = 'U'
)

// Msg is a decoded message. Fields a type does not carry are left zero.
type Msg struct {
	Type      byte
	Side      byte // 'B' buy or 'S' sell, add orders only
	Locate    uint16
	Timestamp uint64 // nanoseconds since midnight
	Ref       uint64 // order reference; the original order for Replace
	NewRef    uint64 // Replace only
	Shares    uint32 // added, executed, canceled or replacement shares
	Price     uint32 // 4 implied decimal places: 1234500 is $123.45
	Stock     [8]byte
	State     byte // SystemEvent event code, or TradingAction trading state
}

// size is the exact length of every message type the book uses.
var size = [256]uint8{
	SystemEvent: 12, StockDirectory: 39, TradingAction: 25, AddOrder: 36, AddOrderMPID: 40,
	Executed: 31, ExecutedPrice: 36, Cancel: 23, Delete: 19, Replace: 35,
}

var ErrShort = errors.New("itch: message shorter than its type requires")

// Decode decodes one message without its length prefix.
func Decode(b []byte) (Msg, error) {
	var m Msg
	if len(b) < 11 || len(b) < int(size[b[0]]) {
		return m, ErrShort
	}
	be := binary.BigEndian
	m.Type = b[0]
	m.Locate = be.Uint16(b[1:])
	m.Timestamp = uint64(be.Uint16(b[5:]))<<32 | uint64(be.Uint32(b[7:]))
	switch m.Type {
	case AddOrder, AddOrderMPID:
		m.Ref = be.Uint64(b[11:])
		m.Side = b[19]
		m.Shares = be.Uint32(b[20:])
		copy(m.Stock[:], b[24:32])
		m.Price = be.Uint32(b[32:])
	case Executed, ExecutedPrice, Cancel:
		m.Ref = be.Uint64(b[11:])
		m.Shares = be.Uint32(b[19:])
	case Delete:
		m.Ref = be.Uint64(b[11:])
	case Replace:
		m.Ref = be.Uint64(b[11:])
		m.NewRef = be.Uint64(b[19:])
		m.Shares = be.Uint32(b[27:])
		m.Price = be.Uint32(b[31:])
	case StockDirectory:
		copy(m.Stock[:], b[11:19])
	case TradingAction:
		copy(m.Stock[:], b[11:19])
		m.State = b[19]
	case SystemEvent:
		m.State = b[11]
	}
	return m, nil
}

// Reader splits a Nasdaq ITCH file, where each message is prefixed with its
// 2-byte big-endian length, into messages.
type Reader struct {
	r   *bufio.Reader
	buf [1 << 16]byte
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 1<<20)}
}

// Next returns the next message without its length prefix. The slice is only
// valid until the next call. It returns io.EOF at a clean end of input.
func (r *Reader) Next() ([]byte, error) {
	if _, err := io.ReadFull(r.r, r.buf[:2]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(r.buf[:2]))
	if _, err := io.ReadFull(r.r, r.buf[:n]); err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	return r.buf[:n], nil
}

// Append encodes m, without a length prefix, onto b. It is the inverse of
// Decode for the types the book uses, and exists for tests and generators.
func Append(b []byte, m Msg) []byte {
	n := int(size[m.Type])
	if n == 0 {
		n = 11
	}
	b = append(b, make([]byte, n)...)
	p := b[len(b)-n:]
	be := binary.BigEndian
	p[0] = m.Type
	be.PutUint16(p[1:], m.Locate)
	be.PutUint16(p[5:], uint16(m.Timestamp>>32))
	be.PutUint32(p[7:], uint32(m.Timestamp))
	switch m.Type {
	case AddOrder, AddOrderMPID:
		be.PutUint64(p[11:], m.Ref)
		p[19] = m.Side
		be.PutUint32(p[20:], m.Shares)
		copy(p[24:32], m.Stock[:])
		be.PutUint32(p[32:], m.Price)
	case Executed, ExecutedPrice, Cancel:
		be.PutUint64(p[11:], m.Ref)
		be.PutUint32(p[19:], m.Shares)
	case Delete:
		be.PutUint64(p[11:], m.Ref)
	case Replace:
		be.PutUint64(p[11:], m.Ref)
		be.PutUint64(p[19:], m.NewRef)
		be.PutUint32(p[27:], m.Shares)
		be.PutUint32(p[31:], m.Price)
	case StockDirectory:
		copy(p[11:19], m.Stock[:])
	case TradingAction:
		copy(p[11:19], m.Stock[:])
		p[19] = m.State
	case SystemEvent:
		p[11] = m.State
	}
	return b
}
