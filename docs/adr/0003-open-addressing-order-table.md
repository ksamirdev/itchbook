# 3. Open-addressing order table instead of a Go map

## Status

Accepted

## Context

Every execute, cancel, delete and replace looks up an order by its reference number, then changes or removes it.
With `map[uint64]order` that is a lookup plus a second hash and probe for the write.
The CPU profile showed map operations at about 60% of the time spent in the book.

## Decision

A custom hash table: linear probing, and backward-shift deletion so deletes leave no tombstones.
`find` returns the slot index; the caller changes the order in place or removes that slot.
The table is sized up front for the expected peak (4M resting orders by default) and pre-faulted, so the first touches of its 192 MB do not page-fault mid-feed.

Nasdaq gives new orders nearly consecutive reference numbers, so the hash keeps each run of 512 consecutive refs in consecutive slots and Fibonacci-hashes the run number to scatter the runs.
On the first 20 million messages I measured three hashes:

| Hash | msg/s | p50 | p99 | p99.9 | p99.99 |
| --- | --- | --- | --- | --- | --- |
| Fibonacci hash of the ref | 6.4M | 125 ns | 419 ns | ~800 ns | ~4.7 µs |
| ref itself (`ref & mask`) | 7.5M | 42 ns | 419 ns | ~5 µs | ~18 µs |
| runs of 512 refs, runs scattered | 8.5M | 42 ns | 375 ns | ~750 ns | ~3.6 µs |

Fibonacci hashing scatters every add to a random line of a 192 MB table.
Using the ref itself puts each add next to the previous one, but survivors from earlier refs make long fully occupied runs, and a backward-shift delete walks to the end of its run.
Runs of 512 keep most of the locality and bound how long a run can grow.
The profile shows time in `find` down by about a quarter; I could not attribute all of the remaining gain.

## Consequences

Against the Go map, throughput rose from 5.1M to 8.5M messages/s and p99 fell from about 630 to 375 ns on the same data.
Growing past the capacity still works but rehashes the whole table during one message; `Books.Grows` reports it.
More code to own than a map, covered by the model test with a 16-slot table so it grows and wraps constantly.
