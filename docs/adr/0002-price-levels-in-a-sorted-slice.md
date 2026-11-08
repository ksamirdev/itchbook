# 2. Price levels in a sorted slice, found by binary search

## Status

Accepted

## Context

Each side of a book needs: find the level for a price, add or take shares, insert a new level, delete an empty one, and read the best price.
Trees and skip lists allocate a node per level and chase pointers.
Most activity is close to the best price.

## Decision

Store each side as a slice of 16-byte levels sorted so the best price is last.
Asks store the complement of the price, so both sides share one code path.
Inserting near the best price moves only the few levels above it.
Find levels with binary search.

I first used a linear scan from the best price, expecting it to win because most updates land near the top.
On the first 20 million messages of the day, binary search was better: p99 dropped from about 500 to 420 ns and p99.9 from about 1.3 µs to 1 µs.
Stale orders far from the price make the linear scan walk hundreds of levels often enough to own the tail.

## Consequences

A level insert or delete far from the best price is a `memmove` of the levels above it.
Levels hold only totals (shares and order count), not the queue of orders, so the book cannot report queue position.
