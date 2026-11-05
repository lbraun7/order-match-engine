# ADR 0005: One single-writer engine per symbol

## Status

Accepted.

## Context

A single engine preserves deterministic price-time priority, but one global
queue makes an overloaded symbol delay unrelated books and prevents matching
work from using more than one CPU. Sharing an order book between workers would
instead weaken the existing single-writer invariant.

## Decision

Use a process-local router that lazily creates exactly one `Engine` per opaque,
case-sensitive symbol. Each engine retains its own bounded queue, worker,
sequence space, book, event log, and recovery snapshot. The transport also
scopes mutation ordering, client idempotency, and market-data subscribers per
symbol. Postgres projection keys and checkpoints include the symbol.

The router serializes only first creation and its lifecycle registry. Normal
commands take a read lock briefly to find the engine and then enter only that
symbol's queue. Shutdown first rejects new routing, then concurrently drains
all initialized engines.

Durable directories use URL-safe base64 encoding of the symbol so opaque user
input cannot escape the configured data root.

## Consequences

- A full or slow queue affects only its symbol.
- Different books can match in parallel while each book still has one writer.
- Sequences, order IDs, trade IDs, and client order IDs are unique only within
  a symbol, so every external lookup must include the symbol.
- Memory and goroutine count grow with the number of active symbols.
- Lazy creation keeps unused symbols cheap but makes first access pay recovery
  and projector startup cost.
- The router is process-local; symbol placement across multiple service
  instances remains future work.
