// Command loadtest drives the public gRPC API and emits a machine-readable
// report with throughput, latency percentiles, parameters, and host metadata.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ordermatchv1 "github.com/lucasbraun96/order-match-engine/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type report struct {
	Timestamp       time.Time         `json:"timestamp"`
	Target          string            `json:"target"`
	RunID           string            `json:"run_id"`
	Requests        int               `json:"requests"`
	Concurrency     int               `json:"concurrency"`
	Symbols         []string          `json:"symbols"`
	RequestTimeout  string            `json:"request_timeout"`
	Successful      uint64            `json:"successful"`
	Failed          uint64            `json:"failed"`
	Elapsed         string            `json:"elapsed"`
	Throughput      float64           `json:"throughput_requests_per_second"`
	Latency         latencyReport     `json:"latency"`
	Environment     environmentReport `json:"environment"`
	FailureExamples []string          `json:"failure_examples,omitempty"`
}

type latencyReport struct {
	P50 string `json:"p50"`
	P95 string `json:"p95"`
	P99 string `json:"p99"`
	Max string `json:"max"`
}

type environmentReport struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	CPU        string `json:"cpu"`
	LogicalCPU int    `json:"logical_cpus"`
	GoVersion  string `json:"go_version"`
}

func main() {
	target := flag.String("target", "localhost:50051", "gRPC server address")
	requests := flag.Int("requests", 10000, "total PlaceOrder calls")
	concurrency := flag.Int("concurrency", 16, "concurrent clients")
	requestTimeout := flag.Duration("request-timeout", 5*time.Second, "timeout for each call")
	runID := flag.String("run-id", fmt.Sprintf("run-%d", time.Now().UnixNano()), "unique order ID prefix for this run")
	symbolList := flag.String("symbols", "BTC-USD", "comma-separated symbols distributed across requests")
	flag.Parse()
	symbols := splitSymbols(*symbolList)
	if *requests <= 0 || *concurrency <= 0 || *requestTimeout <= 0 || len(symbols) == 0 {
		fmt.Fprintln(os.Stderr, "requests, concurrency, request-timeout, and symbols must be valid")
		os.Exit(2)
	}

	connection, err := grpc.NewClient(*target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer connection.Close()
	client := ordermatchv1.NewOrderMatchServiceClient(connection)

	latencies := make(chan time.Duration, *requests)
	start := make(chan struct{})
	var next atomic.Uint64
	var successful atomic.Uint64
	var failed atomic.Uint64
	var failuresMu sync.Mutex
	var failureExamples []string
	var workers sync.WaitGroup
	workerCount := min(*concurrency, *requests)
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			<-start
			for {
				index := int(next.Add(1) - 1)
				if index >= *requests {
					return
				}
				callStarted := time.Now()
				ctx, cancel := context.WithTimeout(context.Background(), *requestTimeout)
				_, callErr := client.PlaceOrder(ctx, request(*runID, symbols[index%len(symbols)], index))
				cancel()
				latencies <- time.Since(callStarted)
				if callErr == nil {
					successful.Add(1)
					continue
				}
				failed.Add(1)
				failuresMu.Lock()
				if len(failureExamples) < 5 {
					failureExamples = append(failureExamples, callErr.Error())
				}
				failuresMu.Unlock()
			}
		}()
	}
	started := time.Now()
	close(start)
	workers.Wait()
	elapsed := time.Since(started)
	close(latencies)
	values := make([]time.Duration, 0, *requests)
	for latency := range latencies {
		values = append(values, latency)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })

	result := report{
		Timestamp: time.Now().UTC(), Target: *target, RunID: *runID, Requests: *requests,
		Concurrency: *concurrency, Symbols: symbols, RequestTimeout: requestTimeout.String(),
		Successful: successful.Load(), Failed: failed.Load(), Elapsed: elapsed.String(),
		Throughput: float64(successful.Load()) / elapsed.Seconds(),
		Latency: latencyReport{
			P50: percentile(values, 50).String(), P95: percentile(values, 95).String(),
			P99: percentile(values, 99).String(), Max: percentile(values, 100).String(),
		},
		Environment: environmentReport{
			OS: runtime.GOOS, Arch: runtime.GOARCH, CPU: cpuModel(),
			LogicalCPU: runtime.NumCPU(), GoVersion: runtime.Version(),
		},
		FailureExamples: failureExamples,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if failed.Load() > 0 {
		os.Exit(1)
	}
}

func request(runID, symbol string, index int) *ordermatchv1.PlaceOrderRequest {
	side := ordermatchv1.Side_SIDE_SELL
	if index%2 == 1 {
		side = ordermatchv1.Side_SIDE_BUY
	}
	id := fmt.Sprintf("%s-%d", runID, index)
	return &ordermatchv1.PlaceOrderRequest{
		ClientOrderId: id, OrderId: id, Symbol: symbol, Side: side,
		OrderType:   ordermatchv1.OrderType_ORDER_TYPE_LIMIT,
		TimeInForce: ordermatchv1.TimeInForce_TIME_IN_FORCE_GTC,
		Price:       10000, Quantity: 1,
	}
}

func splitSymbols(value string) []string {
	var symbols []string
	for _, symbol := range strings.Split(value, ",") {
		if symbol = strings.TrimSpace(symbol); symbol != "" {
			symbols = append(symbols, symbol)
		}
	}
	return symbols
}

func percentile(sorted []time.Duration, value int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := (value*len(sorted) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}

func cpuModel() string {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("sysctl", "-n", "machdep.cpu.brand_string")
	case "linux":
		command = exec.Command("sh", "-c", "sed -n 's/^model name[[:space:]]*: //p' /proc/cpuinfo | head -1")
	default:
		return "unknown"
	}
	output, err := command.Output()
	if err != nil && !errors.Is(err, exec.ErrNotFound) {
		return "unknown"
	}
	model := strings.TrimSpace(string(output))
	if model == "" {
		return "unknown"
	}
	return model
}
