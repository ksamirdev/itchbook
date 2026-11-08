# 3. Open-addressing order table instead of a Go map

## Status

Accepted

## Context

Every execute, cancel, delete and replace looks up an order by its reference number, then changes or removes it.
With `map[uint64]order` that is a lookup plus a second hash and probe for the write.
The CPU profile showed map operations at about 60% of the time spent in the book.

## Decision

A custom hash table: linear probing, Fibonacci hashing of the reference number, and backward-shift deletion so deletes leave no tombstones.
`find` returns the slot index; the caller changes the order in place or removes that slot.
The table is sized up front for the expected peak (4M resting orders by default) and pre-faulted, so the first touches of its 192 MB do not page-fault mid-feed.

## Consequences

Throughput rose from 5.1M to 6.2M messages/s and p99 fell from about 630 to 500 ns on the same data.
Growing past the capacity still works but rehashes the whole table during one message; `Books.Grows` reports it.
More code to own than a map, covered by the model test with a 16-slot table so it grows and wraps constantly.
