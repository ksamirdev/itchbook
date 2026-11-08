// Package hist is a fixed-size latency histogram in the style of
// HdrHistogram: exact below 128ns, then 64 buckets per power of two, so any
// value is reported within 1.6% of the truth. Recording is a few
// instructions and never allocates.
package hist

import "math/bits"

const buckets = 59 * 64

type Hist struct {
	counts [buckets]uint64
	n      uint64
	max    uint64
}

func index(v uint64) int {
	if v < 128 {
		return int(v)
	}
	shift := bits.Len64(v) - 7 // keep the top 7 bits, 64..127
	return shift*64 + int(v>>shift)
}

// upper is the largest value that falls in bucket i.
func upper(i int) uint64 {
	if i < 128 {
		return uint64(i)
	}
	shift := i/64 - 1
	return (uint64(i%64+64)+1)<<shift - 1
}

func (h *Hist) Record(v uint64) {
	h.counts[index(v)]++
	h.n++
	h.max = max(h.max, v)
}

func (h *Hist) Count() uint64 { return h.n }
func (h *Hist) Max() uint64   { return h.max }

// Quantile returns the value q (0..1) of recordings are at or below.
func (h *Hist) Quantile(q float64) uint64 {
	if h.n == 0 {
		return 0
	}
	rank := uint64(q * float64(h.n))
	rank = max(rank, 1)
	var seen uint64
	for i, c := range h.counts {
		if seen += c; seen >= rank {
			return min(upper(i), h.max)
		}
	}
	return h.max
}

func (h *Hist) Merge(o *Hist) {
	for i, c := range o.counts {
		h.counts[i] += c
	}
	h.n += o.n
	h.max = max(h.max, o.max)
}
