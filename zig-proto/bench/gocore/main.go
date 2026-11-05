// Direct Go-core baseline for the ADR 0001 comparison.
//
// Runs the same single-threaded workloads as zig-proto/bench/cbench.c, but
// entirely in-process: the kvlite core with the linked LMDB extension, no C
// ABI crossing, no binding. Comparing this with cbench against the Go host
// isolates the C ABI + binding cost; comparing with the Zig host isolates
// what the Go core adds per operation (envelope encoding, logical-key
// bookkeeping) beyond the engine itself.
//
// Usage (from zig-proto/bench/gocore):
//
//	GOWORK=off go run . --workload put --ops 2000 --value 64 --label gocore
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"kvlite"
	_ "github.com/webong/kvlite/extensions/lmdb"
)

func nowNs() int64 { return time.Now().UnixNano() }

func formatKey(index uint64) string {
	return fmt.Sprintf("k%012d", index)
}

func fillValue(value []byte, seed byte) {
	state := uint64(0x9e3779b97f4a7c15) ^ uint64(seed)
	for i := range value {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		value[i] = byte(state >> 33)
	}
}

func main() {
	workload := flag.String("workload", "put", "put|get|rget|del")
	ops := flag.Int("ops", 2000, "operations")
	keys := flag.Int("keys", 0, "prepopulated key space (default: ops)")
	valueSize := flag.Int("value", 64, "value bytes")
	label := flag.String("label", "gocore", "comparison label")
	dbPath := flag.String("db", "", "database directory (default: fresh temp dir)")
	flag.Parse()

	if *keys == 0 {
		*keys = *ops
	}
	dir := *dbPath
	if dir == "" {
		base, err := os.MkdirTemp("", "kvlite-zig-proto-go-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(base)
		dir = filepath.Join(base, "db")
	}

	db, err := kvlite.Open(dir, kvlite.WithDriver("lmdb"))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	fmt.Printf("info,library=go-core-lmdb-inprocess,driver=lmdb\n")

	value := make([]byte, *valueSize)
	fillValue(value, 7)
	samples := make([]int64, 0, *ops)

	preload := func() {
		for i := 0; i < *keys; i++ {
			if err := db.PutBytes(context.Background(), formatKey(uint64(i)), value); err != nil {
				panic(err)
			}
		}
	}

	start := nowNs()
	switch *workload {
	case "put":
		for i := 0; i < *ops; i++ {
			oneStart := nowNs()
			if err := db.PutBytes(context.Background(), formatKey(uint64(i)), value); err != nil {
				panic(err)
			}
			samples = append(samples, nowNs()-oneStart)
		}
	case "get", "rget":
		preload()
		samples = samples[:0]
		start = nowNs()
		for i := 0; i < *ops; i++ {
			index := uint64(i)
			if *workload == "rget" {
				index = xorshift(uint64(i+1)) % uint64(*keys)
			}
			oneStart := nowNs()
			if _, err := db.GetBytes(context.Background(), formatKey(index)); err != nil {
				panic(err)
			}
			samples = append(samples, nowNs()-oneStart)
		}
	case "del":
		preload()
		samples = samples[:0]
		start = nowNs()
		for i := 0; i < *ops; i++ {
			oneStart := nowNs()
			if err := db.Delete(context.Background(), formatKey(uint64(i))); err != nil {
				panic(err)
			}
			samples = append(samples, nowNs()-oneStart)
		}
	default:
		panic("unknown workload " + *workload)
	}
	seconds := float64(nowNs()-start) / 1e9

	var total int64
	for _, sample := range samples {
		total += sample
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	fmt.Printf("bench,label=%s,workload=%s,threads=1,value=%d,ops=%d,"+
		"seconds=%.3f,ops_per_sec=%.0f,mean_us=%.2f,p50_us=%.2f,p99_us=%.2f\n",
		*label, *workload, *valueSize, len(samples), seconds,
		float64(len(samples))/seconds,
		float64(total)/float64(len(samples))/1000.0,
		float64(sorted[len(sorted)/2])/1000.0,
		float64(sorted[(len(sorted)*99)/100])/1000.0)
}

func xorshift(state uint64) uint64 {
	state ^= state << 13
	state ^= state >> 7
	state ^= state << 17
	return state
}