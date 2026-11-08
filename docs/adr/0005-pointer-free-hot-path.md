# 5. A pointer-free hot path instead of tuning the garbage collector

## Status

Accepted

## Context

Go's garbage collector is the usual reason given for not writing low-latency systems in Go.
Its cost is scanning pointers and freeing what was allocated.

## Decision

Keep the book's state free of pointers: orders and levels are plain structs in slices, indexed by number.
Allocate at start of day, not per message: the order table is sized up front and every stock reserves 64 levels per side when the Stock Directory message announces it.
Check it with `testing.AllocsPerRun` in tests and by counting allocations in the benchmark.

## Consequences

On the first 20 million messages of the day the collector ran 0 times while messages were applied, and `GOGC=off` changed nothing measurable.
Sides deeper than 64 levels still double occasionally; the benchmark reports how many allocations that caused.
The remaining tail comes from cache misses and the operating system, not the runtime.
