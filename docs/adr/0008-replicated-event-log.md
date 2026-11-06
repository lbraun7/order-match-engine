# ADR 0008: Synchronous local event-log replication

## Status

Accepted.

## Context

A single local event log survives process restart but not loss or corruption
of its storage path. Full distributed consensus is beyond this single-process
portfolio service, but a second independently configured path can demonstrate
replication, validation, and deterministic failover semantics.

## Decision

When `replica-data-dir` is configured, each command record is appended and
flushed to the primary and then the replica before the in-memory mutation.
Startup reads both copies:

- identical histories are accepted;
- a missing, corrupt, or truncated copy is atomically rebuilt from its valid
  peer;
- a strict prefix is extended from the longer valid history;
- conflicting valid histories fail startup with `ErrReplicaDiverged`.

The primary snapshot is only a replay optimization. If the primary directory
is lost, symbol discovery also scans the replica and the book can be rebuilt
entirely from its command log.

## Consequences

- Normal acceptance pays for two `fsync` operations.
- A crash between the two flushes creates an uncertain strict prefix. Recovery
  preserves the longer valid history; callers must retry an unanswered command
  using transport idempotency.
- This design has no remote leader, fencing token, quorum, or automatic traffic
  failover. Two paths on one physical host do not protect against host loss.
- Conflicting histories are never merged or guessed.
