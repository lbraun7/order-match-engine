# Order Matching Engine

A deterministic, durable, multi-symbol order matching engine written in
Go. The project demonstrates price-time priority, one single writer per book,
recovery from replicated append-only event logs, asynchronous Postgres projections, a
versioned gRPC API, market-data streaming, and measured performance.

Prices and quantities are integers: the caller owns the fixed-point scale. For
example, `10100` can mean USD 101.00 when the chosen scale is cents.

## Quick start

Requirements: Docker with Compose v2. No local Go installation is needed for
the demo.

```sh
git clone https://github.com/lucasbraun96/order-match-engine.git
cd order-match-engine
docker compose up --build -d
docker compose --profile demo run --rm --no-deps demo
```

The demo waits for the gRPC service, creates an isolated symbol, places a sell
limit for 5 units at `10100`, then a crossing buy for 3 units at `10200`. Its
JSON output contains the resting order, the trade at the maker price (`10100`),
and a final snapshot with 2 units left on the ask. Run it again safely: IDs and
the demo symbol are unique per execution.

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

## Market data and idempotency

Every RPC requires a case-sensitive, opaque `symbol`; `PlaceOrder` also
requires `client_order_id`. Repeating the same client ID and payload in the
same process and symbol returns the original response. Reusing it with a
different payload returns gRPC `ALREADY_EXISTS`.

A market-data subscription receives a snapshot first. Deltas contain
`previous_sequence` and `sequence`; a mismatch is a gap and the client must
reconnect for a fresh snapshot. Slow consumers are disconnected with
`RESOURCE_EXHAUSTED` rather than silently losing updates.

The protobuf contract is [`api/v1/order_match.proto`](api/v1/order_match.proto).

## Test, fuzz, and benchmark

Run the CI-equivalent checks:

```sh
gofmt -w .
go test ./...
go vet ./...
go test -race ./...
```

Run the seeded fuzz corpus as ordinary tests, then explore each target:

```sh
go test ./...
go test -fuzz=FuzzReadAll -fuzztime=30s ./internal/eventlog
go test -fuzz=FuzzCommandSequence -fuzztime=30s ./internal/engine
go test -fuzz=FuzzRestoreOrderBook -fuzztime=30s ./internal/engine
```

The command-sequence target checks determinism, price ordering, uncrossed
books, FIFO, active-index consistency, and quantity conservation. Parser
targets exercise arbitrary event-log bytes and recovery snapshot state.

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
