# ADR 0001: Order book structure

## Status

Accepted

## Decision

Each side keeps a sorted slice of active prices and a map from price to a
`container/list` FIFO queue. Bids sort descending and asks ascending. The book
also keeps an all-orders map for stable identity and an active-order map whose
entries point directly to list elements.

Orders and trades share a monotonic sequence. A trade executes at its maker's
price and receives a deterministic decimal ID derived from its sequence.

## Consequences

Finding the best level and consuming its first order are constant-time. Adding
a new price sorts the price slice, which is simple and adequate before the
project has benchmark evidence for a more specialized structure. The active
index prepares cancellation to remove an order without scanning its level.

The book is mutable and deliberately not concurrency-safe. The single-writer
engine planned for phase 4 will serialize access instead of adding locks to the
matching core.
