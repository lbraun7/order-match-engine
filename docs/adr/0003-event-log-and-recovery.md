# ADR 0003: Durability with a command event log

## Status

Accepted; amended by ADR 0008 when a replica path is configured.

## Decision

The single-writer engine owns an append-only, versioned command log. Each
record contains the sequence at which the command starts, its type, JSON
payload, format version, payload length, and CRC32 checksum. The engine writes
the complete record and calls `fsync` before mutating the order book.

An order is considered accepted when its command record has been completely
written and flushed. It may then execute synchronously in memory. Invalid or
conflicting commands are rejected before logging. If a write is partial or a
flush fails, that log instance becomes unusable and rejects later appends; the
process must be restarted after the underlying storage problem is resolved.

Snapshots contain every accepted order (including terminal orders), the last
engine sequence, and the number of log records they cover. They are written to
a temporary file, flushed, atomically renamed, and followed by a directory
flush. Startup restores a valid snapshot and replays only later log records.
Without a snapshot it replays the entire log. A truncated record, bad magic,
unsupported version, checksum mismatch, sequence gap, or invalid replay stops
startup rather than guessing or silently discarding data.

## Consequences

The log stores client commands instead of derived trades and book deltas. The
same deterministic matcher regenerates those effects during recovery, keeping
the persistence format small and testing determinism directly. Every accepted
mutation pays for one storage flush, favoring clear durability semantics over
throughput at this stage. Group commit can be introduced later without
changing the record format or acceptance boundary.
