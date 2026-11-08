// Package exchange plays the exchange's side of a MoldUDP64 feed: it packs
// recorded ITCH messages into packets and answers re-requests for missed
// messages, the way Nasdaq's re-request server does.
package exchange

import (
	"errors"
	"net"

	"github.com/ksamirdev/itchbook/internal/mold"
)

// Session holds one session's messages. Message seq (from 1) is
// data[offs[seq-1]:offs[seq]].
type Session struct {
	ID   [10]byte
	data []byte
	offs []int
}

func NewSession(id [10]byte) *Session {
	return &Session{ID: id, offs: []int{0}}
}

// Add appends the next message.
func (s *Session) Add(msg []byte) {
	s.data = append(s.data, msg...)
	s.offs = append(s.offs, len(s.data))
}

// Len returns how many messages the session has.
func (s *Session) Len() uint64 { return uint64(len(s.offs) - 1) }

// Msg returns message seq.
func (s *Session) Msg(seq uint64) []byte { return s.data[s.offs[seq-1]:s.offs[seq]] }

// Pack builds the packet starting at seq, with at most limit messages, and
// returns the sequence number after its last message.
func (s *Session) Pack(p *mold.Packer, seq, limit uint64) uint64 {
	p.Session = s.ID
	p.Start(seq)
	end := min(s.Len()+1, seq+limit)
	for ; seq < end && p.Add(s.Msg(seq)); seq++ {
	}
	return seq
}

// EndOfSession is the packet that closes the session.
func (s *Session) EndOfSession() []byte {
	return mold.AppendHeader(nil, mold.Header{Session: s.ID, Seq: s.Len() + 1, Count: mold.EndOfSession})
}

// ServeRequests answers re-requests arriving on conn until it is closed.
func (s *Session) ServeRequests(conn *net.UDPConn) error {
	buf := make([]byte, 64)
	var p mold.Packer
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		h, err := mold.ParseHeader(buf[:n])
		if err != nil || h.Session != s.ID || h.Seq == 0 {
			continue
		}
		end := min(h.Seq+uint64(h.Count), s.Len()+1)
		for seq := h.Seq; seq < end; {
			seq = s.Pack(&p, seq, end-seq)
			if _, err := conn.WriteToUDPAddrPort(p.Packet(), from); err != nil {
				return err
			}
		}
	}
}
