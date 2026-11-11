# Order Matching Engine

[![CI](https://github.com/lbraun7/order-match-engine/actions/workflows/ci.yml/badge.svg)](https://github.com/lbraun7/order-match-engine/actions/workflows/ci.yml)
[![Go 1.24+](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A deterministic, durable, multi-symbol order matching engine written in
Go. The project demonstrates price-time priority, one single writer per book,
recovery from replicated append-only event logs, asynchronous Postgres projections, a
versioned gRPC API, market-data streaming, and measured performance.

Prices and quantities are integers: the caller owns the fixed-point scale. For
example, `10100` can mean USD 101.00 when the chosen scale is cents.

## Why this project

This is a portfolio implementation of the part of an exchange that turns
commands into an ordered stream of book mutations and trades. It is deliberately
more than an in-memory matching algorithm: the repository explores the system
design around that algorithm—serialization, backpressure, crash recovery,
idempotency, read models, streaming market data, observability, and measurable
performance.

The main engineering decisions are:

- **Determinism over shared-state concurrency:** one writer owns each book;
- **Parallelism between instruments:** independent symbols run on independent
  queues and goroutines;
- **Durability before visibility:** a command is flushed to the configured logs
  before it mutates memory or becomes visible to the caller;
- **A small synchronous core:** gRPC, Postgres, Prometheus, and profiling stay
  outside the domain and matching packages;
- **Evidence-driven optimization:** benchmarks and profiles are committed as a
  reproducible process, and an alternative price index was rejected when its
  measured gain did not justify its complexity.

> This is an educational and portfolio system, not a production exchange. It
> has no account ledger, pre-trade risk, settlement, distributed consensus, or
> authentication. The complete boundary is documented in
> [`docs/limitations.md`](docs/limitations.md).

## Contents

- [Quick start](#quick-start)
- [Architecture](#architecture)
- [How a command becomes a trade](#how-a-command-becomes-a-trade)
- [Order semantics](#order-semantics)
- [Durability, recovery, and projections](#durability-recovery-and-projections)
- [API and market data](#api-and-market-data)
- [Run locally](#run-locally)
- [Step-by-step test guide](#step-by-step-test-guide)
- [Performance](#performance)
- [Repository map](#repository-map)

## Quick start

Requirements: Docker with Compose v2. No local Go installation is needed for
the demo.

```sh
git clone https://github.com/lbraun7/order-match-engine.git
cd order-match-engine
docker compose up --build -d
docker compose --profile demo run --rm --no-deps demo
```

The demo waits for the gRPC service, creates an isolated symbol, places a sell
limit for 5 units at `10100`, then a crossing buy for 3 units at `10200`. Its
JSON output contains the resting order, the trade at the maker price (`10100`),
and a final snapshot with 2 units left on the ask. Run it again safely: IDs and
the demo symbol are unique per execution.

The important parts of the output should look like this (generated IDs and
sequences vary):

```text
resting_order.order.status      1  (OPEN)
aggressor.order.status          3  (FILLED)
aggressor.trades[0].price       10100
aggressor.trades[0].quantity    3
final_snapshot.asks[0].price    10100
final_snapshot.asks[0].total_quantity  2
```

The demo uses Go's standard JSON encoder, so protobuf enum values appear as
their numeric wire-compatible values.

The stack includes:

| Component | Address | Purpose |
| --- | --- | --- |
| gRPC API | `localhost:50051` | Commands, queries, and market data |
| Metrics and pprof | <http://localhost:9090> | Prometheus metrics and Go profiles |
| Prometheus | <http://localhost:9091> | Metrics storage and queries |
| Grafana | <http://localhost:3000> | Provisioned dashboard, no login required |
| Postgres | `localhost:5433` | Asynchronous query projections |

Stop the stack with `docker compose down`. Named volumes intentionally retain
the event logs, Postgres data, Prometheus data, and dashboards. To delete those
volumes too, use `docker compose down --volumes` only when that data is no
longer needed.

## Architecture

```mermaid
flowchart LR
    Client[Client] -->|gRPC| API[gRPC API]
    API --> Router[Symbol router]
    Router --> BTC[Single-writer engine<br/>BTC-USD]
    Router --> ETH[Single-writer engine<br/>ETH-USD]
    BTC --> BTCBook[In-memory book]
    ETH --> ETHBook[In-memory book]
    BTC -->|fsync before mutation| Log[(Primary event log)]
    BTC -->|synchronous fsync| Replica[(Replica event log)]
    Log --> Snapshot[(Recovery snapshot)]
    Log --> Projector[Async projector]
    Projector --> Postgres[(Postgres query models)]
    API --> Stream[Snapshot + market-data deltas]
    Metrics[Prometheus / pprof] -. observes .-> Router
```

Each symbol owns a bounded command queue, goroutine, sequence space, book,
event log, recovery snapshot, idempotency registry, market-data subscribers,
and projection checkpoint. Different symbols run in parallel; each individual
book retains exactly one writer.

The core matching packages do not import gRPC, Postgres, or Prometheus. See
[`docs/architecture.md`](docs/architecture.md) for the write/recovery paths and
package boundaries, and the ADRs for the trade-offs behind the design:

- [book structure](docs/adr/0001-order-book-structure.md);
- [single-writer concurrency](docs/adr/0002-single-writer-engine.md);
- [event-log durability](docs/adr/0003-event-log-and-recovery.md);
- [asynchronous projections](docs/adr/0004-asynchronous-postgres-projections.md);
- [multi-symbol routing](docs/adr/0005-multi-symbol-router.md);
- [price-index evidence gate](docs/adr/0006-price-index-evidence.md);
- [advanced order semantics](docs/adr/0007-advanced-order-semantics.md);
- [synchronous log replication](docs/adr/0008-replicated-event-log.md).

## How a command becomes a trade

1. **The gRPC boundary validates transport data.** It converts protobuf values
   into domain commands and rejects malformed symbols, prices, quantities,
   order types, and time-in-force combinations.
2. **The symbol router selects an engine.** A symbol is created lazily and gets
   its own bounded queue, worker goroutine, book, log files, and sequence space.
3. **The queue applies backpressure.** Submission is non-blocking. A full queue
   returns gRPC `RESOURCE_EXHAUSTED`; the service does not hide overload behind
   an unbounded buffer.
4. **The single writer serializes the command.** Only the symbol's engine
   goroutine can read or mutate its order book, eliminating book-level locks and
   making priority deterministic.
5. **The command becomes durable.** The engine appends a checksummed record and
   calls `fsync` on the primary log and, when configured, its replica. A write
   failure prevents the in-memory mutation.
6. **The matcher applies price-time priority.** It consumes the best opposite
   prices first, preserves FIFO within a level, prices trades at the resting
   maker, and rests or cancels the remainder according to the order semantics.
7. **The result fans out.** The RPC receives immutable order/trade snapshots,
   subscribers receive a sequenced market-data delta, and an asynchronous
   projector later updates Postgres without delaying matching.

This ownership model is the central concurrency invariant:

```text
many clients -> one bounded queue per symbol -> one writer -> one order book
                         |                         |
                  overload is explicit      state is deterministic
```

## Order semantics

The matcher accepts GTC/IOC/FOK limit orders, IOC market orders, GTC
stop-market and stop-limit orders, and GTC iceberg orders. Bids sort by
descending price, asks by ascending price, and orders at the same price execute
FIFO. A crossing order trades at the resting maker's price. Client timestamps
never determine priority. Iceberg replenishment and stop activation join the
back of their price level.

Orders can select cancel-aggressor, cancel-resting, or cancel-both self-trade
prevention. `MassCancel` cancels every active and dormant order in a symbol,
optionally filtered by participant; omitting the symbol cancels that participant
across every known book.

Accepted orders, generated trades, and successful cancellations share a
monotonic sequence within one symbol. Order IDs, trade IDs, client order IDs,
and sequences are scoped to that symbol. The book never retains crossed sides,
and every fill conserves quantity.

The complete invariants, lifecycle, cancellation, idempotency, stream-gap, and
durability semantics are in [`docs/domain-semantics.md`](docs/domain-semantics.md).

## Durability, recovery, and projections

The append-only command log is the source of truth; trades and book state are
deterministically derived from it. Periodic atomic snapshots reduce startup
time but never replace the log. On startup the service:

1. discovers persisted symbols from their URL-safe directory names;
2. reconciles the primary and replica logs;
3. repairs a missing, truncated, or corrupt peer when the other copy is valid;
4. restores a valid snapshot; and
5. replays every later command before accepting traffic.

Identical logs and strict-prefix relationships are recoverable. Two valid but
divergent histories are ambiguous, so startup fails instead of guessing. This
replication protects against a damaged local copy; it is not consensus or
automatic failover between hosts.

Postgres contains disposable query models (`orders`, `order_history`, and
`trades`). Each projected event and its checkpoint are committed in one
transaction, making retries idempotent. If Postgres is down, matching continues
and projection catches up from the durable log after the database returns.

## Run locally

Go 1.24 or newer is required.

```sh
go run ./cmd/order-match-engine
```

Then run the same end-to-end example outside Docker:

```sh
go run ./cmd/demo
```

By default durable files live below `./data`, in one URL-safe base64 directory
per symbol. Configure the service with flags such as:

```sh
go run ./cmd/order-match-engine \
  -address=:50051 \
  -data-dir=./data \
  -replica-data-dir=./replica-data \
  -queue-capacity=1024 \
  -snapshot-interval=1000 \
  -database-url='postgres://order_match:order_match@localhost:5433/order_match?sslmode=disable'
```

An order is accepted only after its command record is completely written and
flushed to the primary and, when configured, the replica. Startup reconciles identical or prefix-related
copies, repairs a missing/truncated/corrupt peer from the valid copy, restores a
valid snapshot, and replays the remaining log. Conflicting replica histories,
unsupported records, or non-contiguous sequences stop startup.
Postgres is not on the matching critical path. If it is temporarily
unavailable, bounded retries are logged and later polling resumes from the
unchanged projection checkpoint.

Rebuild all Postgres query tables from the source-of-truth event logs with:

```sh
go run ./cmd/order-match-engine \
  -database-url='postgres://order_match:order_match@localhost:5433/order_match?sslmode=disable' \
  -rebuild-projections
```

## API and market data

Every RPC requires a case-sensitive, opaque `symbol`; `PlaceOrder` also
requires `client_order_id`. Repeating the same client ID and payload in the
same process and symbol returns the original response. Reusing it with a
different payload returns gRPC `ALREADY_EXISTS`.

A market-data subscription receives a snapshot first. Deltas contain
`previous_sequence` and `sequence`; a mismatch is a gap and the client must
reconnect for a fresh snapshot. Slow consumers are disconnected with
`RESOURCE_EXHAUSTED` rather than silently losing updates.

The versioned protobuf contract is
[`api/v1/order_match.proto`](api/v1/order_match.proto) and exposes:

| RPC | Purpose |
| --- | --- |
| `PlaceOrder` | Submit limit, market, stop, or iceberg orders |
| `CancelOrder` | Cancel one open or dormant order |
| `MassCancel` | Cancel a symbol or one participant across known symbols |
| `GetOrder` | Read one order from the serialized engine state |
| `GetBookSnapshot` | Read a consistent visible bid/ask snapshot |
| `StreamMarketData` | Receive a snapshot followed by sequenced deltas |

## Step-by-step test guide

### 1. Portfolio demo (recommended first run)

Requirements: Docker Engine with Compose v2. No local Go toolchain is needed.

```sh
docker compose up --build -d
docker compose ps
docker compose --profile demo run --rm --no-deps demo
```

Confirm the demo reports one trade at `10100` for quantity `3` and leaves an
ask quantity of `2`. Then inspect the running system:

```sh
docker compose logs --tail=100 order-match-engine
curl --fail http://localhost:9090/metrics | grep '^order_match_'
```

Open Grafana at <http://localhost:3000> to see the provisioned dashboard, or
Prometheus at <http://localhost:9091> to query raw time series.

### 2. Unit, recovery, API, and fuzz-seed tests

Requirements: Go 1.24 or newer.

Run the core CI checks:

```sh
test -z "$(gofmt -l .)"
go test ./...
go vet ./...
go test -race ./...
```

`go test ./...` covers the domain model, matching rules, concurrent producers,
queue saturation, snapshots and replay, replica repair/divergence, gRPC error
mapping and idempotency, market-data gaps, projection retry/rebuild behavior,
metrics, and the checked-in fuzz seed corpus. The formatting command is
read-only; use `gofmt -w .` only when you intentionally want to rewrite files.
CI additionally provisions Postgres, applies every migration, and sets
`TEST_DATABASE_URL`, so the database integration test is not silently skipped.

### 3. Real Postgres integration test

The regular suite skips the database-backed test unless `TEST_DATABASE_URL` is
set. Start only Postgres, let Compose apply the migrations, and run it explicitly:

```sh
docker compose up -d postgres

TEST_DATABASE_URL='postgres://order_match:order_match@localhost:5433/order_match?sslmode=disable' \
  go test -count=1 -run '^TestPostgresStoreAppliesEventAtomicallyAndIdempotently$' \
  ./internal/projection
```

This verifies transactional writes, idempotent replay, and checkpoint
advancement against a real database.

Compose initialization scripts run only when the Postgres data directory is
first created. If an older local volume predates the current schema and the
test reports a missing column such as `symbol`, recreate **only the development
volumes** and start Postgres again (this permanently deletes their local data):

```sh
docker compose down --volumes
docker compose up -d postgres
```

### 4. Explore fuzz targets

Each command below runs one target for 30 seconds:

```sh
go test -fuzz=FuzzReadAll -fuzztime=30s ./internal/eventlog
go test -fuzz=FuzzCommandSequence -fuzztime=30s ./internal/engine
go test -fuzz=FuzzRestoreOrderBook -fuzztime=30s ./internal/engine
```

The command-sequence target checks determinism, price ordering, uncrossed
books, FIFO, active-index consistency, and quantity conservation. Parser
targets exercise arbitrary event-log bytes and recovery snapshot state.

### 5. End-to-end load test

With the service running, exercise the public gRPC path, queue, durable logs,
and matcher together:

```sh
go run ./cmd/loadtest \
  -target=localhost:50051 \
  -requests=10000 \
  -concurrency=16 \
  -run-id="$(date +%s)"
```

The command prints a JSON report with throughput, p50/p95/p99/max latency and
failure count, and exits nonzero if any request failed.

### 6. Clean up

```sh
docker compose down
```

This preserves named volumes so recovery can be tested on the next startup.
To erase all local engine, replica, Postgres, Prometheus, and Grafana data, use
`docker compose down --volumes` deliberately.

## Performance

Reproduce matcher benchmarks and profiles with:

```sh
./scripts/benchmark.sh
./scripts/profile.sh
```

[`docs/performance.md`](docs/performance.md) records the environment,
parameters, reference results, allocations, limitations, profiles, and the
measured optimization. The numbers are evidence from one machine, not a
throughput promise.

## Repository map

```text
api/v1/                  protobuf contract and generated Go API
cmd/demo/                complete public-API example
cmd/loadtest/            end-to-end gRPC load generator
cmd/order-match-engine/  service entry point
internal/domain/         value types, order lifecycle, and invariants
internal/engine/         matcher, single writer, recovery, and symbol router
internal/eventlog/       checksummed log and atomic snapshots
internal/grpcserver/     transport, idempotency, and market data
internal/observability/  Prometheus collectors and gRPC instrumentation
internal/projection/     asynchronous Postgres query-model writer
migrations/              versioned Postgres schema
deploy/                  Prometheus and Grafana configuration
docs/                    architecture, semantics, ADRs, and measurements
```

Known limitations and concrete next steps are documented in
[`docs/limitations.md`](docs/limitations.md).

## License

This project is available under the [MIT License](LICENSE).
