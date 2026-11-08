// Command bench replays a Nasdaq ITCH 5.0 file through the order books and
// reports how long each message took to decode and apply.
//
// The file is decompressed in large chunks between timed sections, so the
// histogram measures the book, not gzip.
package main

import (
	"compress/gzip"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/ksamirdev/itchbook/internal/book"
	"github.com/ksamirdev/itchbook/internal/hist"
	"github.com/ksamirdev/itchbook/internal/itch"
)

func main() {
	file := flag.String("file", "data/12302019.NASDAQ_ITCH50.gz", "ITCH 5.0 file, gzipped or not")
	limit := flag.Int("limit", 0, "stop after this many messages, 0 for the whole file")
	chunk := flag.Int("chunk", 64<<20, "bytes decompressed between timed sections")
	cpuprofile := flag.String("cpuprofile", "", "write a CPU profile of the timed sections here")
	memprofile := flag.String("memprofile", "", "write a profile of every allocation here")
	capacity := flag.Int("capacity", 4<<20, "resting orders to size the order table for")
	check := flag.Bool("check", false, "after each message, count books crossed while trading (untimed)")
	flag.Parse()

	f, err := os.Open(*file)
	if err != nil {
		log.Fatal(err)
	}
	var in io.Reader = f
	if strings.HasSuffix(*file, ".gz") {
		if in, err = gzip.NewReader(f); err != nil {
			log.Fatal(err)
		}
	}
	if *cpuprofile != "" {
		pf, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(pf); err != nil {
			log.Fatal(err)
		}
	}
	if *memprofile != "" {
		runtime.MemProfileRate = 1
	}

	r := run{books: book.New(*capacity), check: *check, limit: *limit}
	buf := make([]byte, *chunk+1<<16)
	var tail int // bytes of a partial message carried into the next chunk
	var applied time.Duration
	var before, after, m0, m1 runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	for !r.done() {
		n, err := io.ReadFull(in, buf[tail:*chunk])
		if err != nil && err != io.EOF && !errors.Is(err, io.ErrUnexpectedEOF) {
			log.Fatal(err)
		}
		if n == 0 {
			break
		}
		runtime.ReadMemStats(&m0)
		t := time.Now()
		used := r.chunk(buf[:tail+n])
		applied += time.Since(t)
		runtime.ReadMemStats(&m1)
		r.allocs += m1.Mallocs - m0.Mallocs // the book's, not gzip's
		tail = copy(buf, buf[used:tail+n])
		if err != nil {
			break
		}
	}
	wall := time.Since(start)
	runtime.ReadMemStats(&after)
	pprof.StopCPUProfile()
	if *memprofile != "" {
		pf, err := os.Create(*memprofile)
		if err != nil {
			log.Fatal(err)
		}
		runtime.GC() // the profile only includes allocations up to the last GC
		if err := pprof.Lookup("allocs").WriteTo(pf, 0); err != nil {
			log.Fatal(err)
		}
	}
	r.report(os.Stdout, wall, applied, &before, &after)
}

type run struct {
	books   *book.Books
	lat     hist.Hist
	types   [256]uint64
	errs    map[error]uint64
	crossed uint64
	allocs  uint64
	check   bool
	limit   int
	n       int
}

func (r *run) done() bool { return r.limit > 0 && r.n >= r.limit }

// chunk applies every whole message in b and returns how many bytes it used.
func (r *run) chunk(b []byte) int {
	off := 0
	for off+2 <= len(b) && !r.done() {
		size := int(binary.BigEndian.Uint16(b[off:]))
		if off+2+size > len(b) {
			break
		}
		msg := b[off+2 : off+2+size]
		off += 2 + size

		t := time.Now()
		m, err := itch.Decode(msg)
		if err == nil {
			err = r.books.Apply(&m)
		}
		r.lat.Record(uint64(time.Since(t)))

		r.n++
		r.types[msg[0]]++
		if err != nil {
			if r.errs == nil {
				r.errs = map[error]uint64{}
			}
			r.errs[err]++
		}
		if r.check && m.Locate != 0 {
			b := r.books.Book(m.Locate)
			bid, okb := b.Best(false)
			ask, oka := b.Best(true)
			if okb && oka && bid.Price >= ask.Price && b.Trading() {
				r.crossed++
			}
		}
	}
	return off
}

func (r *run) report(w io.Writer, wall, applied time.Duration, before, after *runtime.MemStats) {
	fmt.Fprintf(w, "messages        %d in %v (%.1fM msg/s through the book, %v wall)\n",
		r.n, applied.Round(time.Millisecond), float64(r.n)/applied.Seconds()/1e6, wall.Round(time.Millisecond))
	fmt.Fprintf(w, "latency ns      p50 %d  p90 %d  p99 %d  p99.9 %d  p99.99 %d  max %d\n",
		r.lat.Quantile(.5), r.lat.Quantile(.9), r.lat.Quantile(.99), r.lat.Quantile(.999), r.lat.Quantile(.9999), r.lat.Max())
	fmt.Fprintf(w, "clock           %s\n", clockResolution())
	fmt.Fprintf(w, "gc              %d cycles, %v total pause, %d allocations while applying (%.6f per message)\n",
		after.NumGC-before.NumGC, time.Duration(after.PauseTotalNs-before.PauseTotalNs),
		r.allocs, float64(r.allocs)/float64(max(r.n, 1)))
	fmt.Fprintf(w, "resting orders  %d at the end, order table grew %d times\n", r.books.Orders(), r.books.Grows())
	fmt.Fprintf(w, "types          ")
	for t, c := range r.types {
		if c > 0 {
			fmt.Fprintf(w, " %c:%d", t, c)
		}
	}
	fmt.Fprintln(w)
	for err, c := range r.errs {
		fmt.Fprintf(w, "error           %d x %v\n", c, err)
	}
	if r.check {
		fmt.Fprintf(w, "crossed books   %d messages left a trading book crossed\n", r.crossed)
	}
}

// clockResolution reports the smallest step time.Now can measure, which
// bounds how finely the histogram can resolve a single message.
func clockResolution() string {
	var h hist.Hist
	for range 1_000_000 {
		t := time.Now()
		h.Record(uint64(time.Since(t)))
	}
	smallest := uint64(0)
	for range 1000 {
		t := time.Now()
		for {
			if d := uint64(time.Since(t)); d > 0 {
				if smallest == 0 || d < smallest {
					smallest = d
				}
				break
			}
		}
	}
	return fmt.Sprintf("smallest tick %dns, empty measurement p50 %dns p99 %dns", smallest, h.Quantile(.5), h.Quantile(.99))
}
