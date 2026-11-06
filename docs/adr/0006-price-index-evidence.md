# ADR 0006: Keep the sorted price index after measuring alternatives

## Status

Accepted.

## Context

The roadmap permits a ring buffer or specialized price structure only when a
benchmark justifies the extra invariants. The current index is a sorted slice:
binary search is O(log n), inserting a new level shifts O(n) entries, best
price access is O(1), and snapshots already have price order.

A ring buffer does not fit sparse arbitrary integer prices. A binary heap is a
plausible alternative for best-price matching, but arbitrary empty-level
removal requires an auxiliary index or tombstones, and ordered snapshots must
copy and sort all keys.

## Evidence

`BenchmarkPriceIndexAlternatives` compared insertion into independent sorted
slices and min-heaps on an Apple M4 with Go 1.27.0 and a one-second benchmark
window on 2026-11-06:

| Levels | Sorted slice | Binary heap | Heap allocations |
| ---: | ---: | ---: | ---: |
| 100 | 126.3 ns/op | 144.5 ns/op | 2 vs 1 |
| 1,000 | 1,099 ns/op | 1,002 ns/op | 3 vs 1 |
| 10,000 | 6,869 ns/op | 5,648 ns/op | 3 vs 1 |

The heap was slower at normal small depth and improved only the isolated
10,000-level insertion by about 18%, while increasing allocations and omitting
its snapshot and arbitrary-removal costs.

## Decision

Keep the sorted slice. Retain the comparison benchmark as the evidence gate
for future representative workloads. A specialized index will be adopted only
if end-to-end profiles show new-level insertion materially limits the service,
not merely because its asymptotic insertion is better.

## Consequences

The simple price-order invariant remains directly inspectable and snapshots do
not need sorting. Very deep, rapidly changing books retain O(n) new-level
insertion; this is documented rather than hidden.
