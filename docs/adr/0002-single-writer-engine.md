# ADR 0002: Single-writer engine

## Status

Accepted

## Context

`OrderBook` uses mutable maps, sorted price slices, and linked FIFO queues. Adding
locks to each operation would make atomic matching and consistent snapshots
harder to reason about and could allow command ordering to depend on client
timestamps.

## Decision

Each book is owned by one engine goroutine. Producers submit place, cancel, and
query commands through one bounded FIFO channel. Each request carries a buffered
result channel, allowing an accepted command to finish even if its caller stops
waiting. The engine returns immutable snapshots instead of mutable order
pointers.

Submission is non-blocking: a full channel returns `ErrQueueFull`. Shutdown first
rejects new submissions, then drains accepted requests and stops the owner
goroutine. A caller context controls enqueue eligibility and result/shutdown
waiting; canceling it does not roll back a command already accepted into the
queue.

## Consequences

- Book mutations and sequence allocation have one writer and require no locks.
- Queue capacity bounds memory and makes overload visible to callers.
- Concurrent producers are serialized by channel arrival order; callers that
  need a particular business order must submit commands in that order.
- Slow matching delays later mutations and queries for the same book.
