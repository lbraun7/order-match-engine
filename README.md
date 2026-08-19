# Order Matching Engine

Order Matching Engine is a learning-focused implementation of a deterministic
order book in Go. The project aims to demonstrate price-time priority,
single-writer concurrency, recovery through an append-only event log, and
measurable performance without coupling the matching core to transport or
storage concerns.

## Current scope

The repository currently includes the foundation and domain phases. It provides
validated integer value types, order enums, orders, trades, placement and
cancellation commands, stable domain errors, and automated quality checks.
Matching, persistence, and APIs will be introduced incrementally according to
the local roadmap.

The initial architecture separates the executable from the core packages:

```text
cmd/order-match-engine/  application entry point
internal/domain/         domain types and invariants
internal/engine/         order matching and command processing
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
- Phase 1 accepts limit orders. The time-in-force vocabulary already includes
  GTC, IOC, and FOK; their execution semantics are implemented in later phases.

## Requirements

- Go 1.24 or newer

## Development

Run the same checks used by continuous integration:

```sh
gofmt -w .
go test ./...
go vet ./...
go test -race ./...
```

Formatting follows `gofmt`. Code must pass `go vet`, tests, and the race
detector before it is merged. New behavior should include idiomatic,
table-driven tests where appropriate; lint exceptions should be narrow and
documented next to the affected code.
