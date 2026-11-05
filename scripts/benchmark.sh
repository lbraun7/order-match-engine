#!/bin/sh
set -eu

output=${1:-artifacts/benchmarks.txt}
mkdir -p "$(dirname "$output")"
{
	go version
	go env GOOS GOARCH
	if command -v getconf >/dev/null 2>&1; then
		getconf _NPROCESSORS_ONLN
	fi
	if command -v sysctl >/dev/null 2>&1; then
		sysctl -n machdep.cpu.brand_string 2>/dev/null || true
	elif [ -r /proc/cpuinfo ]; then
		sed -n 's/^model name[[:space:]]*: //p' /proc/cpuinfo | head -1
	fi
	echo "parameters: count=5 bench=BenchmarkOrderBook benchmem=true"
	go test -run '^$' -bench 'BenchmarkOrderBook' -benchmem -count=5 ./internal/engine
} | tee "$output"
