package hist

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func TestBucketsAreContiguous(t *testing.T) {
	for v := uint64(0); v < 1<<20; v++ {
		i := index(v)
		if v > upper(i) || (i > 0 && v <= upper(i-1)) {
			t.Fatalf("value %d in bucket %d, which holds (%d, %d]", v, i, upper(i-1), upper(i))
		}
	}
	if index(^uint64(0)) != buckets-1 {
		t.Fatalf("max value lands in bucket %d of %d", index(^uint64(0)), buckets)
	}
}

func TestQuantileWithinError(t *testing.T) {
	var h Hist
	r := rand.New(rand.NewPCG(1, 2))
	vals := make([]uint64, 100000)
	for i := range vals {
		vals[i] = uint64(r.ExpFloat64() * 5000)
		h.Record(vals[i])
	}
	slices.Sort(vals)
	for _, q := range []float64{0.5, 0.9, 0.99, 0.999} {
		want := vals[int(q*float64(len(vals)))-1]
		got := h.Quantile(q)
		if got < want || float64(got) > float64(want)*1.016+1 {
			t.Errorf("p%v: got %d, exact %d", q*100, got, want)
		}
	}
	if h.Quantile(1) != vals[len(vals)-1] {
		t.Errorf("p100 %d, max %d", h.Quantile(1), vals[len(vals)-1])
	}
}
