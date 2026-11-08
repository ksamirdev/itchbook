// Package feed turns MoldUDP64 packets into order book updates.
//
// Exchanges send every packet twice, on line A and line B, so one line losing
// a packet usually costs nothing: whichever copy arrives first is applied
// and the other is a duplicate. When both lines lose the same packet, the
// handler asks the exchange's re-request server for the missing messages.
//
// Messages are applied strictly in sequence. A packet that arrives after a
// gap is dropped, and the re-request covers it too, so the handler never
// buffers or allocates. See ADR 0004.
package feed

import (
	"errors"
	"time"

	"github.com/ksamirdev/itchbook/internal/book"
	"github.com/ksamirdev/itchbook/internal/itch"
	"github.com/ksamirdev/itchbook/internal/mold"
)

// maxRequest is the most messages one re-request may ask for.
const maxRequest = 0xFFFE

const maxRetryAfter = 100 * time.Millisecond

// Stats counts what happened to packets and messages.
type Stats struct {
	Packets    uint64 // packets received on any line
	Duplicates uint64 // packets whose messages were all applied already
	Ahead      uint64 // packets dropped because an earlier message was missing
	Gaps       uint64 // times a missing message was first noticed
	Requests   uint64 // re-requests sent
	Messages   uint64 // messages applied
	BadMsgs    uint64 // messages the decoder or book rejected
}

type Handler struct {
	Books *book.Books
	// RetryAfter is how long to wait for a re-request to be answered before
	// asking again. Each retry for the same range waits twice as long, up to
	// maxRetryAfter, so a slow answer is not buried under copies of itself.
	RetryAfter time.Duration
	Stats      Stats

	session  [10]byte
	started  bool
	next     uint64 // sequence number of the next message to apply
	seen     uint64 // one past the highest sequence number the exchange has sent
	ended    bool
	reqEnd   uint64    // one past the last message the last re-request asked for
	quietAt  time.Time // when the last re-request was sent or answered in part
	wait     time.Duration
	lastNext uint64
	req      [mold.HeaderLen]byte
}

func New(books *book.Books) *Handler {
	return &Handler{Books: books, RetryAfter: 2 * time.Millisecond, next: 1}
}

var ErrWrongSession = errors.New("feed: packet from another session")

// Next returns the sequence number of the next message to apply.
func (h *Handler) Next() uint64 { return h.next }

// Done reports whether the session ended and every message was applied.
func (h *Handler) Done() bool { return h.ended && h.next >= h.seen }

// Packet applies one packet from either line or the re-request server. If
// messages are missing it returns a re-request to send, or nil.
func (h *Handler) Packet(p []byte, now time.Time) ([]byte, error) {
	hdr, err := mold.ParseHeader(p)
	if err != nil {
		return nil, err
	}
	if !h.started {
		h.session, h.started = hdr.Session, true
	} else if hdr.Session != h.session {
		return nil, ErrWrongSession
	}
	h.Stats.Packets++

	count := uint64(hdr.Count)
	if hdr.Count == mold.EndOfSession {
		h.ended, count = true, 0
	}
	// A heartbeat or end of session carries the next sequence number the
	// exchange will use, so it reveals a gap at the tail too.
	end := hdr.Seq + count
	hadGap := h.next < h.seen
	h.seen = max(h.seen, end)

	switch {
	case hdr.Seq > h.next:
		if count > 0 {
			h.Stats.Ahead++
		}
	case end <= h.next:
		if count > 0 {
			h.Stats.Duplicates++
		}
	default:
		if err := h.apply(p[mold.HeaderLen:], hdr.Seq); err != nil {
			return nil, err
		}
	}
	if !hadGap && h.next < h.seen {
		h.Stats.Gaps++
	}
	return h.request(now), nil
}

// apply applies the messages of a packet body starting at seq, skipping the
// ones already applied.
func (h *Handler) apply(body []byte, seq uint64) error {
	for ; len(body) > 0; seq++ {
		msg, rest, err := mold.Next(body)
		if err != nil {
			return err
		}
		body = rest
		if seq < h.next {
			continue
		}
		m, err := itch.Decode(msg)
		if err == nil {
			err = h.Books.Apply(&m)
		}
		if err != nil {
			h.Stats.BadMsgs++
		}
		h.Stats.Messages++
		h.next = seq + 1
	}
	return nil
}

// request returns a re-request for the missing messages, unless the answer
// to the last one may still be arriving. The slice is reused by the next call.
func (h *Handler) request(now time.Time) []byte {
	if h.next != h.lastNext { // progress: the answer is flowing, restart the clock
		h.lastNext, h.quietAt, h.wait = h.next, now, h.RetryAfter
	}
	if h.next >= h.seen {
		return nil
	}
	if h.next < h.reqEnd { // still waiting for messages already asked for
		if now.Sub(h.quietAt) < h.wait {
			return nil
		}
		h.wait = min(2*h.wait, maxRetryAfter) // nothing came: ask again, later each time
	}
	n := min(h.seen-h.next, maxRequest)
	h.quietAt, h.reqEnd = now, h.next+n
	h.Stats.Requests++
	return mold.AppendHeader(h.req[:0], mold.Header{Session: h.session, Seq: h.next, Count: uint16(n)})
}
