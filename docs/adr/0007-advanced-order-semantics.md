# ADR 0007: Advanced orders remain deterministic derived state

## Status

Accepted.

## Decision

The durable log continues to store client commands rather than derived fills,
tranche refreshes, stop activations, or STP cancellations. Limit IOC/FOK,
market IOC, stop-market, stop-limit, iceberg, and mass-cancel behavior is part
of the deterministic order-book reducer.

FOK performs a no-mutation liquidity scan before execution. Market and IOC
remainders become canceled. Stops are dormant queryable orders and activate
from the last engine trade. Iceberg replenishment moves the new visible tranche
to the back of its level. The incoming order owns the STP policy. Mass cancel
is one symbol-scoped command with an optional participant filter.
The transport may apply participant mass-cancel across known symbols in lexical
order, but does not claim cross-symbol atomicity.

Placement results include every affected order so transport streams and
Postgres projections can represent maker fills, stop activation, and STP
cancellation without reconstructing them independently.

## Consequences

- Replay remains compact and validates determinism across richer behavior.
- Changing any execution semantic requires an event-format/version migration,
  because old commands must keep their historical reducer behavior.
- Stop activation and iceberg refresh have later queue position even though
  the order's immutable acceptance sequence remains unchanged.
- No wall clock participates in matching, expiry, or triggering.
