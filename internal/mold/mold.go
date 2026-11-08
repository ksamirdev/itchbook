// Package mold encodes and decodes MoldUDP64, the UDP framing Nasdaq sends
// ITCH in. Spec: https://www.nasdaqtrader.com/content/technicalsupport/specifications/dataproducts/moldudp64.pdf
//
// A packet is a 20-byte header (session, sequence number of its first
// message, message count) followed by length-prefixed messages. The same
// header alone, sent to the exchange's re-request server, asks for missed
// messages again.
package mold

import (
	"encoding/binary"
	"errors"
)

const (
	HeaderLen = 20
	// EndOfSession in the count field means the session is over.
	EndOfSession = 0xFFFF
	// MaxPayload keeps a packet inside a 1500-byte Ethernet frame.
	MaxPayload = 1400
)

type Header struct {
	Session [10]byte
	Seq     uint64 // sequence number of the first message
	Count   uint16 // messages in the packet; 0 is a heartbeat
}

var ErrMalformed = errors.New("mold: malformed packet")

func ParseHeader(p []byte) (Header, error) {
	if len(p) < HeaderLen {
		return Header{}, ErrMalformed
	}
	var h Header
	copy(h.Session[:], p)
	h.Seq = binary.BigEndian.Uint64(p[10:])
	h.Count = binary.BigEndian.Uint16(p[18:])
	return h, nil
}

func AppendHeader(b []byte, h Header) []byte {
	b = append(b, h.Session[:]...)
	b = binary.BigEndian.AppendUint64(b, h.Seq)
	return binary.BigEndian.AppendUint16(b, h.Count)
}

// Next splits the first message off a packet body.
func Next(body []byte) (msg, rest []byte, err error) {
	if len(body) < 2 {
		return nil, nil, ErrMalformed
	}
	n := int(binary.BigEndian.Uint16(body)) + 2
	if len(body) < n {
		return nil, nil, ErrMalformed
	}
	return body[2:n], body[n:], nil
}

// Packer builds packets from consecutive messages.
type Packer struct {
	Session [10]byte
	buf     []byte
	seq     uint64
	count   uint16
}

// Start begins a packet whose first message has sequence number seq.
func (p *Packer) Start(seq uint64) {
	p.buf = AppendHeader(p.buf[:0], Header{Session: p.Session, Seq: seq})
	p.seq, p.count = seq, 0
}

// Add appends msg if it fits and reports whether it did.
func (p *Packer) Add(msg []byte) bool {
	if p.count > 0 && len(p.buf)+2+len(msg) > HeaderLen+MaxPayload {
		return false
	}
	p.buf = binary.BigEndian.AppendUint16(p.buf, uint16(len(msg)))
	p.buf = append(p.buf, msg...)
	p.count++
	binary.BigEndian.PutUint16(p.buf[18:], p.count)
	return true
}

// Packet returns the packet so far. It is reused by the next Start.
func (p *Packer) Packet() []byte { return p.buf }

func (p *Packer) Count() uint16 { return p.count }
