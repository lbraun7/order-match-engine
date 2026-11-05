#!/bin/sh
set -eu

directory=${1:-artifacts/profiles}
mkdir -p "$directory"
go test -run '^$' -bench 'BenchmarkOrderBookAddPriceLevel/levels=10000$' -benchtime=5s \
	-o "$directory/engine-profile.test" \
	-cpuprofile "$directory/cpu.pprof" \
	-memprofile "$directory/heap.pprof" \
	-blockprofile "$directory/block.pprof" \
	-mutexprofile "$directory/mutex.pprof" \
	./internal/engine

echo "Profiles written to $directory"
echo "Inspect one with: go tool pprof -http=:0 $directory/cpu.pprof"
echo "For goroutines in a running service: curl -o $directory/goroutine.pprof 'http://localhost:9090/debug/pprof/goroutine?debug=0'"
