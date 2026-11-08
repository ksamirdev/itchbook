# 4. Apply strictly in sequence, re-request instead of buffering

## Status

Accepted

## Context

Nasdaq sends ITCH as MoldUDP64 over UDP on two identical lines, A and B.
Packets can be lost on either line, duplicated, or arrive out of order.
A packet that arrives after a gap cannot be applied until the gap is filled.

## Decision

Keep one number, the next sequence number to apply.
Apply a packet's messages from that point, skip messages already applied, and drop any packet that starts after it.
When a gap is seen, ask the re-request server for everything from the next number up to the highest one seen, and ask again only if nothing arrives within `RetryAfter`.
Whichever line delivers a packet first wins; the copy from the other line is a duplicate.

## Consequences

The handler never buffers, so it never allocates and has no buffer to size or overflow.
Packets dropped while a gap is open are fetched again with the gap, so a long gap costs extra re-request traffic.
Losing the same packet on both lines (about 1 in 10,000 packets at 1% loss per line) costs a round trip to the re-request server.
