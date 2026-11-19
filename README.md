# Order Matching Engine

[![CI](https://github.com/lbraun7/order-match-engine/actions/workflows/ci.yml/badge.svg)](https://github.com/lbraun7/order-match-engine/actions/workflows/ci.yml)
[![Go 1.24+](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A production-inspired, deterministic order matching engine written in Go. It
explores how the core of an exchange can process concurrent order flow while
preserving price-time priority, durability, reproducible recovery, and strict
per-symbol ordering.

The repository is designed as a systems-engineering portfolio project. It goes
beyond an in-memory matching algorithm to demonstrate single-writer
concurrency, explicit backpressure, replicated event logs, crash recovery,
asynchronous read models, versioned APIs, observable operations, and
evidence-driven performance work.

Prices and quantities are integers: the caller owns the fixed-point scale. For
example, `10100` can mean USD 101.00 when the chosen scale is cents.

## Why this project

An exchange matching engine must make concurrent requests behave as one
deterministic sequence of state changes. This project focuses on that systems
problem: turning client commands into durable, ordered book mutations and
trades without coupling the matching core to transport, query storage, or
monitoring infrastructure.

## Engineering highlights

| Challenge | Decision | Result |
| --- | --- | --- |
| Deterministic command ordering | One goroutine owns each symbol's order book | Matching and FIFO priority never depend on lock timing or client timestamps |
| Multi-core scalability | Symbols use independent queues and single-writer engines | Instruments run in parallel without multiple writers inside one book |
| Overload protection | Submission uses bounded, non-blocking queues | Backpressure is explicit instead of becoming unbounded memory growth |
| Durable acceptance | Commands are checksummed and `fsync`ed before memory changes | A successful mutation has a clear persistence boundary |
| Storage damage and restart | Replicated logs are reconciled, then snapshots and deterministic replay restore state | Missing, truncated, and corrupt copies can be repaired without guessing divergent histories |
| Database availability | PostgreSQL projections run asynchronously from the event log | Matching continues while query models are unavailable or catching up |
| Market-data consistency | Streams begin with a snapshot and carry contiguous sequence metadata | Consumers can detect gaps and reconnect instead of silently accepting incomplete state |
| Performance decisions | Benchmarks, fuzzing, race tests, and profiles are reproducible | Optimizations are accepted or rejected using measured evidence |

> This is a production-inspired educational and portfolio system, not a
> production exchange. It intentionally focuses on matching, ordering,
> durability, and recovery; it does not include an account ledger, pre-trade
> risk, settlement, distributed consensus, or authentication. The complete
> boundary is documented in
> [`docs/limitations.md`](docs/limitations.md).

## Contents

- [Engineering highlights](#engineering-highlights)
- [Quick start](#quick-start)
- [Matching example](#matching-example)
- [Architecture](#architecture)
- [How a command becomes a trade](#how-a-command-becomes-a-trade)
- [Order semantics](#order-semantics)
- [Durability, recovery, and projections](#durability-recovery-and-projections)
- [API and market data](#api-and-market-data)
- [Architectural decisions](#architectural-decisions)
- [Run locally](#run-locally)
- [Testing](#testing)
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

## Matching example

Suppose the book already contains this resting order:

| Role | Side | Limit price | Quantity | Status |
| --- | --- | ---: | ---: | --- |
| Maker | Sell | `10100` | `5` | Resting on the ask side |

A new order arrives:

| Role | Side | Limit price | Quantity | Behavior |
| --- | --- | ---: | ---: | --- |
| Taker | Buy | `10200` | `3` | Crosses the best available ask |

The engine executes `3` units at `10100`, the resting maker's price. The buy
order becomes filled, while `2` units of the sell order remain at the front of
the ask side:

```text
trade             3 @ 10100
incoming buy      FILLED
remaining ask     2 @ 10100
```

This illustrates the core rules: the best price executes first, FIFO decides
between orders at the same price, and trades use the maker's price. The book
cannot remain crossed after the command completes.

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
[`docs/architecture.md`](docs/architecture.md) for the complete write and
recovery paths and package boundaries.

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

## Architectural decisions

The repository includes Architecture Decision Records (ADRs) so that design
choices, rejected alternatives, and accepted costs remain reviewable instead
of being documented only after implementation.

| Decision | Rationale | Accepted trade-off |
| --- | --- | --- |
| [Sorted price slices with mapped FIFO levels](docs/adr/0001-order-book-structure.md) | Best-level access stays simple while linked lists preserve FIFO and support direct cancellation | Inserting a new price level requires an O(n) slice shift |
| [One writer per order book](docs/adr/0002-single-writer-engine.md) | Serial ownership makes matching, snapshots, and priority deterministic without fine-grained locks | One slow command delays later work for the same symbol |
| [Durable command event log](docs/adr/0003-event-log-and-recovery.md) | Compact client commands can deterministically regenerate orders, fills, and book state | Every accepted mutation pays for a synchronous storage flush |
| [Asynchronous PostgreSQL projections](docs/adr/0004-asynchronous-postgres-projections.md) | Query storage cannot make matching unavailable or add database latency to acceptance | Read models may temporarily lag behind the source-of-truth log |
| [Independent engines per symbol](docs/adr/0005-multi-symbol-router.md) | Instruments scale across CPU cores while retaining one writer inside each book | There is no atomic mutation across symbols |
| [Evidence gate for the price index](docs/adr/0006-price-index-evidence.md) | A heap was slower at shallow depth and only about 18% faster for isolated 10,000-level insertion | The simpler sorted slice remains until representative workloads justify extra complexity |
| [Advanced orders as deterministic derived state](docs/adr/0007-advanced-order-semantics.md) | Stops, icebergs, FOK, and STP can be reproduced from the original command stream | Semantic changes require explicit event-version compatibility |
| [Synchronous local log replication](docs/adr/0008-replicated-event-log.md) | A second path demonstrates repair and corruption handling without claiming distributed consensus | Two `fsync` operations add latency and still do not provide host-level failover |

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

The durability, recovery, and projection behavior described above is enabled by
these flags; PostgreSQL remains optional and outside the matching critical
path.

Rebuild all Postgres query tables from the source-of-truth event logs with:

```sh
go run ./cmd/order-match-engine \
  -database-url='postgres://order_match:order_match@localhost:5433/order_match?sslmode=disable' \
  -rebuild-projections
```

## Testing

The CI pipeline applies the database migrations and runs formatting checks,
the complete Go test suite, static analysis, and the race detector. The test
strategy covers different failure surfaces rather than relying only on happy
path unit tests:

| Evidence | What it verifies |
| --- | --- |
| Domain and matcher tests | Validation, price-time priority, FIFO, quantity conservation, advanced orders, and cancellation |
| Concurrent-producer and queue tests | Single-writer serialization, bounded backpressure, shutdown, and symbol isolation |
| Recovery and replica tests | Snapshot replay, checksum validation, repair of missing or damaged copies, and rejection of divergent histories |
| gRPC and stream tests | Error mapping, idempotency, multi-symbol behavior, sequenced deltas, and slow-consumer disconnection |
| PostgreSQL integration test | Transactional projection updates, idempotent retries, and checkpoint advancement |
| Race detector and fuzz targets | Shared-state safety, arbitrary command sequences, snapshot restoration, and event-log parsing |
| Benchmarks and load generator | Matcher cost, symbol scalability, allocations, and durable end-to-end latency |

### 1. End-to-end demo

Start with the [quick start](#quick-start), confirm one trade at `10100` for
quantity `3`, and verify that `2` units remain on the ask. Then inspect the
running system:

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

Reference measurements are reported separately for the in-memory matcher and
the durable public path because they measure different systems:

| Scope | Reference result |
| --- | ---: |
| Rest one limit order in memory | `701.6 ns/op` |
| Match one maker and taker in memory | `1.293 µs/op` |
| Cancel one resting order in memory | `742.4 ns/op` |
| Durable gRPC path, 16 clients | `247.4 requests/s`, `75.1 ms` p99 |

These results were captured on an Apple M4 with Go 1.27. The matcher numbers
exclude gRPC and disk I/O; the end-to-end result includes synchronous event-log
flushes, which intentionally dominate latency. They are reproducible evidence
from one environment, not capacity promises for other hardware or durability
settings.

Reproduce matcher benchmarks and profiles with:

```sh
./scripts/benchmark.sh
./scripts/profile.sh
```

[`docs/performance.md`](docs/performance.md) records the full methodology,
parameters, allocations, profiles, limitations, and measured optimization.

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
