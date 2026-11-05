# Performance and observability

This document makes Phase 8 measurements reproducible. Results are evidence
for this implementation and machine, not a throughput promise for other
hardware or durability settings.

## Measurement environment

The reference run was captured on 2026-11-04 with:

- Apple M4, 10 logical CPUs, darwin/arm64;
- Go 1.27.0 toolchain (the module remains compatible with Go 1.24);
- matcher benchmarks with `-count=3` and the standard Go benchmark harness;
- end-to-end gRPC service with a 1,024-command queue, synchronous event-log
  flush, snapshots disabled, 16 clients, and 1,000 orders.

Record a fresh environment and five benchmark samples with:

```sh
./scripts/benchmark.sh
```

The output is written to `artifacts/benchmarks.txt` by default and includes the
Go version, OS, architecture, CPU model when the OS exposes it, parameters,
time per operation, bytes per operation, and allocations per operation.

## Reference results

Median values from three matcher samples:

| Operation | Before | After | After allocations |
| --- | ---: | ---: | ---: |
| Rest one limit order | 247.3 ns/op | 231.6 ns/op | 1,128 B/op, 12 allocs/op |
| Match one maker and taker | 353.7 ns/op | 337.4 ns/op | 1,336 B/op, 15 allocs/op |
| Cancel one resting order | 300.4 ns/op | 283.7 ns/op | 1,128 B/op, 12 allocs/op |

The isolated sorted-price-index benchmark after the change measured 73.5 ns
at 100 levels, 629 ns at 1,000 levels, and 4.14 us at 10,000 levels. The
reported bytes are primarily the copied slice used to keep benchmark
iterations independent.

The end-to-end durable run produced:

| Requests | Concurrency | Throughput | p50 | p95 | p99 | Failures |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,000 | 16 | 247.4 requests/s | 64.0 ms | 70.9 ms | 75.1 ms | 0 |

Reproduce the load test against a running service with a unique run ID:

```sh
go run ./cmd/loadtest \
  -target localhost:50051 \
  -requests 10000 \
  -concurrency 16 \
  -run-id "$(date +%s)"
```

The JSON report records throughput, nearest-rank p50/p95/p99/max latency,
failures, all load parameters, hardware, OS, architecture, logical CPUs, and
Go version. IDs alternate sell and buy at the same price so the test exercises
matching without allowing the book to grow indefinitely. A nonzero failure
count makes the command fail, and up to five errors are included in the report.

The reference E2E result is dominated by the intentionally synchronous event
log flush. Filesystem, power policy, other processes, queue size, snapshot
interval, database projection, and client/server placement materially change
the result. Compare runs only when those parameters are the same.

## Multi-symbol scalability

The router benchmark submits concurrently through the public router API while
varying the number of independently owned books. A reference run on 2026-11-05
used Apple M4, 10 logical CPUs, darwin/arm64, Go 1.27.0, and `-benchtime=1s`:

| Symbols | Time/op | Approx. operations/s | Bytes/op | Allocations/op |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 821.4 ns | 1.22 M | 818 B | 12 |
| 2 | 760.2 ns | 1.32 M | 831 B | 12 |
| 4 | 677.5 ns | 1.48 M | 797 B | 12 |
| 8 | 539.4 ns | 1.85 M | 895 B | 12 |

Reproduce it with:

```sh
go test -run '^$' -bench '^BenchmarkRouterSymbolScalability$' \
  -benchmem -benchtime=1s ./internal/engine
```

This run improved throughput as more single writers could execute on separate
CPUs, but scaling was not linear and allocation noise varies between samples.
Results include command validation, routing, queueing, matching, and immutable
result creation, but exclude durable event-log flush and gRPC.

## Profiling and the measured improvement

Generate CPU, heap, block, and mutex profiles for the 10,000-level insertion
benchmark with:

```sh
./scripts/profile.sh
go tool pprof -http=:0 artifacts/profiles/cpu.pprof
```

The identified price-index bottleneck was sorting the complete level slice
whenever a new price appeared. The previous implementation called
`sort.Slice`, doing more comparisons than needed and adding an allocation even
for the common empty-book insertion. The price slice is already ordered, so
the implementation now uses binary search to find the insertion point and one
slice shift. This changes the search/sort work from O(n log n) to O(log n)
search plus the unavoidable O(n) contiguous shift. The basic operations above
improved by roughly 5-6% and each uses one fewer allocation on the reference
machine.

For a live service, the same HTTP endpoint provides all standard Go profiles:

```sh
go tool pprof http://localhost:9090/debug/pprof/profile?seconds=30
go tool pprof http://localhost:9090/debug/pprof/heap
go tool pprof http://localhost:9090/debug/pprof/block
go tool pprof http://localhost:9090/debug/pprof/goroutine
```

The pprof endpoint has no authentication. Keep the observability listener on a
trusted network or disable it with `-observability-address=''`.

## Metrics, Prometheus, and Grafana

`/metrics` exposes:

- `order_match_grpc_requests_total` for completed request throughput;
- `order_match_grpc_request_duration_seconds` for latency histograms;
- `order_match_engine_queue_depth` and queue capacity by symbol;
- `order_match_book_price_levels` by symbol and side, plus resting-order count;
- engine sequence by symbol and stats-scrape failures;
- standard Go runtime and process metrics.

Book and queue gauges are sampled through a serialized aggregate query. No
orders are copied, and the core has no Prometheus dependency. Streaming RPC
duration is excluded because a healthy subscription may be intentionally
long-lived.

Start the service, Prometheus, and provisioned Grafana dashboard with:

```sh
docker compose up --build order-match-engine prometheus grafana
```

The service metrics are at <http://localhost:9090/metrics>, Prometheus at
<http://localhost:9091>, and Grafana at <http://localhost:3000>. The dashboard
shows request rate, p50/p95/p99, queue utilization, price levels, and resting
orders.
