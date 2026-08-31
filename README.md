# Order Matching Engine

Order Matching Engine is a learning-focused implementation of a deterministic
order book in Go. The project aims to demonstrate price-time priority,
single-writer concurrency, recovery through an append-only event log, and
measurable performance without coupling the matching core to transport or
storage concerns.

## Current scope

The repository currently includes the foundation, domain, limit-order
matching, cancellation, queries, the single-writer engine, and a versioned
gRPC API with market-data streaming. It provides a bounded command queue with
explicit overload errors, context-aware operations, and graceful shutdown.
Order queries and consistent book snapshots return immutable value copies.
Accepted mutations are persisted to a checksummed append-only event log and
recovered from periodic snapshots plus replay of subsequent commands.

The initial architecture separates the executable from the core packages:

```text
cmd/order-match-engine/  application entry point
api/v1/                  protobuf contract and generated Go API
internal/domain/         domain types and invariants
internal/engine/         order matching and command processing
internal/grpcserver/     transport, idempotency, and market data
```

The core uses integers for prices and quantities, assigns ordering sequences
inside the engine, and follows price-time priority. Transport, databases, and
observability remain outside the core.

## Domain semantics

- `Price` and `Quantity` are signed integers and must be positive. Callers
  choose their fixed-point scale (for example, cents or basis points).
- IDs must be non-empty and sequences start at one.
- Commands never carry an ordering timestamp or sequence. The engine assigns a
  monotonic sequence when it processes a command, so client clocks cannot alter
  priority.
- Orders expose state through accessors and can change only through validated
  fill and cancellation transitions. A rejected transition leaves the order
  unchanged.
- Domain errors are stable sentinel values and can be matched with `errors.Is`.
- The matcher accepts GTC limit orders. The time-in-force vocabulary already
  includes IOC and FOK, but the matcher rejects them explicitly until their
  execution semantics are implemented.
- Bids are ordered from highest to lowest price and asks from lowest to highest.
  Orders at the same price execute FIFO by their engine-assigned sequence.
- Trades execute at the resting maker's price. Accepted orders, trades, and
  successful cancellations share one monotonic sequence; the decimal trade ID
  is derived from its sequence.
- `OrderBook` is intentionally not concurrency-safe and is owned by the
  `Engine` goroutine when used concurrently.
- The engine serializes placement, cancellation, and queries through a bounded
  FIFO queue. A full queue returns `ErrQueueFull`; shutdown rejects new work and
  drains commands that were already accepted.
- Canceling a caller context stops waiting but does not roll back a command
  already accepted by the engine.
- Cancellation removes an active order through its indexed list element, keeps
  the remaining price level FIFO, and removes empty price levels.
- Order queries include terminal orders. Book snapshots contain price-priority
  levels, aggregate remaining quantity, and resting orders in FIFO order.

## Requirements

- Go 1.24 or newer

## Development

Start the service on its default gRPC address (`:50051`):

```sh
go run ./cmd/order-match-engine
```

By default durable files are stored in `./data`. Use `-data-dir` to select a
different location and `-snapshot-interval` to tune snapshot frequency. An
order is accepted only after its command is flushed to the event log; startup
fails explicitly if a record is truncated or corrupt.

`PlaceOrder` requires a `client_order_id`. Repeating the same ID and payload
returns the original response without touching the engine; reusing it with a
different payload returns `ALREADY_EXISTS`. This registry is process-local and
is intentionally separate from the durable engine command log.

Every market-data subscription receives a book snapshot first. Each delta has
`previous_sequence` and `sequence` high-water marks and can include an accepted
order plus all trades it generated. A mismatch is a gap, so the client must
reconnect for a fresh snapshot. A subscriber whose bounded buffer fills is
disconnected with `RESOURCE_EXHAUSTED` instead of silently losing messages.

Regenerate the checked-in protobuf bindings with:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
PATH="$(go env GOPATH)/bin:$PATH" protoc \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  api/v1/order_match.proto
```

Run the same checks used by continuous integration:

```sh
gofmt -w .
go test ./...
go vet ./...
go test -race ./...
go test -run '^$' -bench . -benchmem ./internal/engine
```

Formatting follows `gofmt`. Code must pass `go vet`, tests, and the race
detector before it is merged. New behavior should include idiomatic,
table-driven tests where appropriate; lint exceptions should be narrow and
documented next to the affected code.
