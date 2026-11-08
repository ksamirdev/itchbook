package exchange

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/ksamirdev/itchbook/internal/mold"
)

func TestServeRequestsOverUDP(t *testing.T) {
	s := NewSession([10]byte{'S', '1'})
	for i := range 500 {
		s.Add(bytes.Repeat([]byte{byte(i)}, 1+i%40))
	}
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	go func() { _ = s.ServeRequests(server) }()

	c, err := net.DialUDP("udp", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	// Ask for 400 messages starting at 90; the session only has 500, so the
	// answer must stop at 500 and span several packets.
	if _, err := c.Write(mold.AppendHeader(nil, mold.Header{Session: s.ID, Seq: 90, Count: 500})); err != nil {
		t.Fatal(err)
	}
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	for want := uint64(90); want <= 500; {
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("waiting for seq %d: %v", want, err)
		}
		if n > mold.HeaderLen+mold.MaxPayload {
			t.Fatalf("packet of %d bytes", n)
		}
		h, err := mold.ParseHeader(buf[:n])
		if err != nil || h.Seq != want || h.Session != s.ID {
			t.Fatalf("got header %+v, %v; want seq %d", h, err, want)
		}
		body := buf[mold.HeaderLen:n]
		for range h.Count {
			msg, rest, err := mold.Next(body)
			if err != nil || !bytes.Equal(msg, s.Msg(want)) {
				t.Fatalf("seq %d: got %v, %v", want, msg, err)
			}
			body, want = rest, want+1
		}
	}
}
