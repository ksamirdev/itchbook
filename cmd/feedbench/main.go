// Command feedbench runs an exchange and a feed handler over real UDP
// sockets on loopback and measures how long each packet takes from the
// exchange's send to the handler's books being updated.
//
// The exchange sends every packet on line A and line B, dropping each copy
// with the given probability, and answers re-requests. Both sides run in one
// process so they share a monotonic clock. At the end the handler's books
// are checked against books built by applying every message once, in order.
package main

import (
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"runtime"
	"runtime/trace"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ksamirdev/itchbook/internal/book"
	"github.com/ksamirdev/itchbook/internal/exchange"
	"github.com/ksamirdev/itchbook/internal/feed"
	"github.com/ksamirdev/itchbook/internal/hist"
	"github.com/ksamirdev/itchbook/internal/itch"
	"github.com/ksamirdev/itchbook/internal/mold"
)

func main() {
	file := flag.String("file", "data/12302019.NASDAQ_ITCH50.gz", "ITCH 5.0 file, gzipped or not")
	messages := flag.Int("messages", 2_000_000, "messages to replay from the start of the file")
	rate := flag.Float64("rate", 500_000, "messages per second the exchange sends")
	loss := flag.Float64("loss", 0.01, "probability each line drops each packet")
	mode := flag.String("mode", "spin", "spin: one thread busy-polls every socket; block: a goroutine per socket hands packets over a channel")
	seed := flag.Uint64("seed", 1, "seed for packet loss")
	traceFile := flag.String("trace", "", "write a Go execution trace here")
	flag.Parse()

	if *traceFile != "" {
		f, err := os.Create(*traceFile)
		if err != nil {
			log.Fatal(err)
		}
		if err := trace.Start(f); err != nil {
			log.Fatal(err)
		}
	}

	s, err := load(*file, *messages)
	if err != nil {
		log.Fatal(err)
	}
	lineA, lineB, rewinder := listen(), listen(), listen()
	reqConn, err := net.DialUDP("udp", nil, rewinder.LocalAddr().(*net.UDPAddr))
	if err != nil {
		log.Fatal(err)
	}
	setBuffers(reqConn)
	go func() {
		if err := s.ServeRequests(rewinder); err != nil {
			log.Fatal(err)
		}
	}()

	r := &receiver{h: feed.New(book.New(4 << 20)), sentAt: make([]atomic.Int64, s.Len()+2), start: time.Now()}
	done := make(chan struct{})
	go func() {
		conns := []*net.UDPConn{lineA, lineB, reqConn}
		if *mode == "spin" {
			r.spin(conns)
		} else {
			r.block(conns)
		}
		close(done)
	}()

	sent := send(s, r, lineA, lineB, *rate, *loss, *seed, done)
	<-done
	trace.Stop()

	fmt.Printf("mode            %s, %d messages at %.0f msg/s, %.1f%% loss per line\n", *mode, s.Len(), *rate, *loss*100)
	fmt.Printf("sent            %d packets per line in %v\n", sent, r.finished.Round(time.Millisecond))
	fmt.Printf("send to book    p50 %d  p90 %d  p99 %d  p99.9 %d  max %d ns (%d packets applied from a line)\n",
		r.lat.Quantile(.5), r.lat.Quantile(.9), r.lat.Quantile(.99), r.lat.Quantile(.999), r.lat.Max(), r.lat.Count())
	fmt.Printf("read to book    p50 %d  p90 %d  p99 %d  p99.9 %d  max %d ns\n",
		r.handler.Quantile(.5), r.handler.Quantile(.9), r.handler.Quantile(.99), r.handler.Quantile(.999), r.handler.Max())
	fmt.Printf("handler         %+v\n", r.h.Stats)
	fmt.Printf("recovered       %d messages from re-requests\n", r.recovered)
	if err := audit(s, r.h.Books); err != nil {
		log.Fatal(err)
	}
	fmt.Println("audit           books match applying every message once, in order")
}

func load(file string, n int) (*exchange.Session, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	var in io.Reader = f
	if strings.HasSuffix(file, ".gz") {
		if in, err = gzip.NewReader(f); err != nil {
			return nil, err
		}
	}
	s := exchange.NewSession([10]byte{'I', 'T', 'C', 'H', 'B', 'O', 'O', 'K', '0', '1'})
	rd := itch.NewReader(in)
	for range n {
		msg, err := rd.Next()
		if err != nil {
			return nil, err
		}
		s.Add(msg)
	}
	return s, nil
}

func listen() *net.UDPConn {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		log.Fatal(err)
	}
	setBuffers(c)
	return c
}

func setBuffers(c *net.UDPConn) {
	// Loopback drops whatever overflows the socket buffer, which would show
	// up as loss nobody injected.
	if err := c.SetReadBuffer(4 << 20); err != nil {
		log.Fatal(err)
	}
	if err := c.SetWriteBuffer(4 << 20); err != nil {
		log.Fatal(err)
	}
}

// send paces packets out on both lines, then sends end of session until the
// handler has everything. It returns the packets sent per line.
func send(s *exchange.Session, r *receiver, lineA, lineB *net.UDPConn, rate, loss float64, seed uint64, done chan struct{}) int {
	rng := rand.New(rand.NewPCG(seed, 0))
	out, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		log.Fatal(err)
	}
	setBuffers(out)
	a := lineA.LocalAddr().(*net.UDPAddr).AddrPort()
	b := lineB.LocalAddr().(*net.UDPAddr).AddrPort()
	var p mold.Packer
	packets := 0
	for seq := uint64(1); seq <= s.Len(); packets++ {
		first := seq
		seq = s.Pack(&p, seq, 1<<16)
		// Busy-wait for the packet's slot: sleeping wakes late and sends in
		// bursts, which the receiver would see as queueing.
		due := time.Duration(float64(first-1) / rate * 1e9)
		for time.Since(r.start) < due {
		}
		r.sentAt[first].Store(int64(time.Since(r.start)))
		for _, to := range []netip.AddrPort{a, b} {
			if rng.Float64() >= loss {
				if _, err := out.WriteToUDPAddrPort(p.Packet(), to); err != nil {
					log.Fatal(err)
				}
			}
		}
	}
	eos := s.EndOfSession()
	for {
		select {
		case <-done:
			return packets
		case <-time.After(time.Millisecond):
			for _, to := range []netip.AddrPort{a, b} {
				_, _ = out.WriteToUDPAddrPort(eos, to)
			}
		}
	}
}

type receiver struct {
	h         *feed.Handler
	sentAt    []atomic.Int64 // send time of the packet starting at each seq
	start     time.Time
	lat       hist.Hist // exchange send to books updated
	handler   hist.Hist // socket read returned to books updated
	recovered uint64
	finished  time.Duration
}

// handle applies one packet. Packets from the lines are timed against their
// send; re-request answers start at arbitrary seqs and are only counted.
// readAt is when the socket read returned, relative to start.
func (r *receiver) handle(pkt []byte, fromLine bool, req *net.UDPConn, readAt int64) {
	before := r.h.Next()
	ask, err := r.h.Packet(pkt, time.Now())
	if err != nil {
		log.Fatal(err)
	}
	if r.h.Next() > before {
		hdr, _ := mold.ParseHeader(pkt)
		if !fromLine {
			r.recovered += r.h.Next() - before
		} else if hdr.Seq == before {
			now := int64(time.Since(r.start))
			r.lat.Record(uint64(now - r.sentAt[hdr.Seq].Load()))
			r.handler.Record(uint64(now - readAt))
		}
	}
	if ask != nil {
		if _, err := req.Write(ask); err != nil {
			log.Fatal(err)
		}
	}
	if r.h.Done() {
		r.finished = time.Since(r.start)
	}
}

// spin polls every socket without blocking from one locked OS thread, so a
// packet is picked up as soon as the kernel has it and never waits for the
// Go scheduler to wake a goroutine.
func (r *receiver) spin(conns []*net.UDPConn) {
	runtime.LockOSThread()
	raws := make([]syscall.RawConn, len(conns))
	for i, c := range conns {
		var err error
		if raws[i], err = c.SyscallConn(); err != nil {
			log.Fatal(err)
		}
	}
	buf := make([]byte, 2048)
	var n int
	var rerr error
	read := func(fd uintptr) bool {
		n, rerr = syscall.Read(int(fd), buf)
		return true // never park: EAGAIN just means try the next socket
	}
	for !r.h.Done() {
		for i, raw := range raws {
			if err := raw.Read(read); err != nil {
				log.Fatal(err)
			}
			if rerr == nil && n > 0 {
				r.handle(buf[:n], i < 2, conns[2], int64(time.Since(r.start)))
			} else if rerr != nil && !errors.Is(rerr, syscall.EAGAIN) {
				log.Fatal(rerr)
			}
		}
	}
}

type packet struct {
	buf      *[2048]byte
	n        int
	fromLine bool
	readAt   int64
}

// block reads each socket in its own goroutine, parked by the Go netpoller
// until data arrives, and hands packets to one goroutine that owns the books.
func (r *receiver) block(conns []*net.UDPConn) {
	free := make(chan *[2048]byte, 4096)
	for range cap(free) {
		free <- new([2048]byte)
	}
	full := make(chan packet, cap(free))
	for i, c := range conns {
		go func() {
			for {
				b := <-free
				n, err := c.Read(b[:])
				if err != nil {
					return // closed when the run ends
				}
				full <- packet{b, n, i < 2, int64(time.Since(r.start))}
			}
		}()
	}
	for !r.h.Done() {
		p := <-full
		r.handle(p.buf[:p.n], p.fromLine, conns[2], p.readAt)
		free <- p.buf
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// audit checks the handler's books against applying every message in order.
func audit(s *exchange.Session, got *book.Books) error {
	want := book.New(4 << 20)
	for seq := uint64(1); seq <= s.Len(); seq++ {
		m, _ := itch.Decode(s.Msg(seq))
		_ = want.Apply(&m)
	}
	if got.Orders() != want.Orders() {
		return fmt.Errorf("audit: %d resting orders, want %d", got.Orders(), want.Orders())
	}
	for locate := range 1 << 16 {
		for _, sell := range []bool{false, true} {
			g := got.Book(uint16(locate)).Depth(sell, 1<<30)
			w := want.Book(uint16(locate)).Depth(sell, 1<<30)
			if !slices.Equal(g, w) {
				return fmt.Errorf("audit: locate %d sell %v differs", locate, sell)
			}
		}
	}
	return nil
}
