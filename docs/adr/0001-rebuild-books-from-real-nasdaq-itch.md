# 1. Rebuild books from a real Nasdaq ITCH day

## Status

Accepted

## Context

A trading firm does not run its own matching engine.
It receives the exchange's market-data feed and rebuilds every stock's order book from it, as fast as possible.
Synthetic order flow would make the book easy to write and the numbers easy to flatter, because real flow has deep books, stale orders far from the price, halted stocks and bursts.

## Decision

Decode Nasdaq TotalView-ITCH 5.0, using the free full-day sample files Nasdaq publishes, and rebuild a full-depth book for every stock.
Measure everything against one real day (30 December 2019).
Keep a deliberately simple model book in the tests and check the fast book against it on random streams and with Go's fuzzer.

## Consequences

The data is 3.5 GB compressed and is not committed; `just data` downloads it.
CI cannot replay the real day, so CI relies on the model and fuzz tests.
Only the message types that change the book are decoded; trades, imbalances and the rest are skipped by type.
