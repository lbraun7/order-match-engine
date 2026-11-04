# ADR 0004: Asynchronous Postgres projections

## Status

Accepted.

## Context

The matcher must remain deterministic and available when the query database is
slow or unavailable. Trades and order history are derived from already durable
commands, so making command acceptance depend on Postgres would introduce a
second synchronous durability boundary and couple matching latency to database
health.

## Decision

The append-only event log is the source of truth. A separate projector tails
that log, deterministically replays commands through an isolated order book,
and writes query models to Postgres. Each database transaction inserts the
durable event sequence as an idempotency marker, persists every affected order
state and trade, and advances one checkpoint.

Database operations use a bounded retry budget. Exhaustion is logged and
counted, then a later polling cycle tries again from the unchanged checkpoint.
The service can also reset all projection tables and replay the complete event
log.

## Consequences

- Matching does not wait for or require Postgres.
- Query tables are eventually consistent with the event log.
- Duplicate delivery is safe because event and trade identifiers are unique.
- Rebuilds are deterministic and do not need matcher snapshots.
- A reader can briefly observe stale data while the projector catches up.
