# Known limitations and next steps

This is a portfolio and learning implementation, not a production exchange.
The boundaries below are intentional and should be considered before any
deployment handling real value.

## Matching and instruments

- Amend/replace, trailing stops, pegged orders, auction orders, and participant
  risk limits are not implemented. Stop triggers use the engine's last trade;
  there is no external consolidated market price.
- Symbols are opaque strings. There is no instrument catalog enforcing tick
  size, lot size, currency, trading status, or per-instrument risk limits.
- There is no participant, account, balance, pre-trade risk, fee, or settlement
  model, and no atomic operation across symbols.
- The sorted price slice has O(n) insertion cost for a new level. A specialized
  tree or ring structure should replace it only after representative profiles
  justify the extra complexity.

## Availability and durability

- The router is process-local. The second event-log copy provides local
  synchronous redundancy and startup repair, not distributed consensus,
  leader election, fencing, quorum, or automatic process failover. Both Docker
  volumes may still share one physical host.
- Every accepted mutation performs two synchronous log flushes when replication
  is enabled. This gives a clear
  durability boundary but dominates end-to-end throughput; group commit is a
  possible next step.
- Logs are append-only and have no online compaction, retention, archival, or
  operator tooling for backup and restore.
- Existing legacy single-symbol files and old Postgres schemas require an
  explicit operator migration; the service refuses to infer symbol ownership.

## API and operations

- Client-order idempotency is held in memory and is lost on process restart.
  Durable idempotency would require reconstructing or persisting the registry.
- Market data has no replay-by-offset endpoint. On a gap or slow-consumer
  disconnect, the client must request a new snapshot and continue from there.
- There is no TLS, authentication, authorization, rate limiting, audit actor,
  or tenant isolation. The metrics and pprof endpoint is unauthenticated.
- Lazy symbol creation has no cardinality quota or idle eviction, so untrusted
  callers could create unbounded engines, goroutines, and directories.
- Postgres projections are eventually consistent and are not exposed through
  a dedicated public query API in this repository.
- A crash after one replica flush but before the second can leave a strict
  prefix. Startup preserves and repairs the longer valid history, so clients
  must treat a lost RPC response as uncertain and retry idempotently.

## Verification still valuable

- Add fault-injection tests for disk-full, lost fsync, process termination at
  durability boundaries, and long Postgres outages.
- Add model-based/property testing against a small reference matcher and keep
  long-running fuzz corpora in CI.
- Benchmark realistic price distributions, cancellation ratios, symbol counts,
  durable storage devices, and networked clients before changing data
  structures or publishing capacity claims.
- Add rolling-upgrade compatibility tests for protobuf, event-log, snapshot,
  and database schema versions.
