// Binding-level benchmarks for the ADR 0001 host comparison.
//
// These measure the Go binding (root package) on top of any C ABI v1
// library: the Zig prototype or the Go-built driver bundle. Set:
//
//   KVLITE_BENCH_LIBRARY=/path/to/libkvlite
//   KVLITE_BENCH_LABEL=zig|go-host
//
// and run for example:
//
//   go test -run '^$' -bench BenchmarkBinding -cpu 1,4 .
//
// Every operation on one *DB crosses loadedDB's per-database mutex, so the
// -cpu N concurrent runs quantify that mutex directly: any drop from N-way
// parallel speedup here, compared against the same workloads through the C
// ABI in zig-proto/bench/cbench.c, is binding serialization.
//
// Skipped entirely when KVLITE_BENCH_LIBRARY is unset.

//go:build cgo && (linux || (darwin && !amd64) || windows)

package kvlite

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func benchLibrary(t *testing.B) string {
	t.Helper()
	library := os.Getenv("KVLITE_BENCH_LIBRARY")
	if library == "" {
		t.Skip("set KVLITE_BENCH_LIBRARY to the library to benchmark")
	}
	return library
}

func benchLabel(t *testing.B) string {
	t.Helper()
	if label := os.Getenv("KVLITE_BENCH_LABEL"); label != "" {
		return label
	}
	return "unset"
}

func benchDB(t *testing.B) (*DB, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "db"), WithLibraryPath(benchLibrary(t)))
	if err != nil {
		t.Fatal(err)
	}
	return db, func() { _ = db.Close() }
}

func benchKey(index int) []byte {
	return []byte(fmt.Sprintf("k%012d", index))
}

func benchValue(size int) []byte {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		panic(err)
	}
	return payload
}

func BenchmarkBindingPut(b *testing.B) {
	label := benchLabel(b)
	for _, size := range []int{64, 4096} {
		b.Run(fmt.Sprintf("%s/value=%d", label, size), func(b *testing.B) {
			db, closeDB := benchDB(b)
			defer closeDB()
			value := benchValue(size)
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := db.PutBytes(benchKey(i), value, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkBindingGet(b *testing.B) {
	label := benchLabel(b)
	for _, size := range []int{64, 4096} {
		b.Run(fmt.Sprintf("%s/value=%d", label, size), func(b *testing.B) {
			db, closeDB := benchDB(b)
			defer closeDB()
			value := benchValue(size)
			const preloaded = 10000
			for i := 0; i < preloaded; i++ {
				if err := db.PutBytes(benchKey(i), value, 0); err != nil {
					b.Fatal(err)
				}
			}
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := db.GetBytes(benchKey(i%preloaded)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkBindingDelete(b *testing.B) {
	label := benchLabel(b)
	b.Run(fmt.Sprintf("%s/value=64", label), func(b *testing.B) {
		db, closeDB := benchDB(b)
		defer closeDB()
		value := benchValue(64)
		// Prepopulate inside setup time so the timed loop measures deletes.
		keys := make([][]byte, b.N)
		for i := 0; i < b.N; i++ {
			keys[i] = benchKey(i)
			if err := db.PutBytes(keys[i], value, 0); err != nil {
				b.Fatal(err)
			}
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := db.DeleteBytes(keys[i]); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkBindingParallelGet measures read throughput when many goroutines
// share one *DB handle — the per-database mutex in native_dynamic.go
// serializes these calls.
func BenchmarkBindingParallelGet(b *testing.B) {
	label := benchLabel(b)
	for _, size := range []int{64, 4096} {
		b.Run(fmt.Sprintf("%s/value=%d", label, size), func(b *testing.B) {
			db, closeDB := benchDB(b)
			defer closeDB()
			value := benchValue(size)
			const preloaded = 10000
			for i := 0; i < preloaded; i++ {
				if err := db.PutBytes(benchKey(i), value, 0); err != nil {
					b.Fatal(err)
				}
			}
			b.SetBytes(int64(size))
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				index := 0
				for pb.Next() {
					if _, err := db.GetBytes(benchKey(index%preloaded)); err != nil {
						b.Fatal(err)
					}
					index++
				}
			})
		})
	}
}

// BenchmarkBindingOpenClose captures cold startup through the binding:
// open a fresh directory, write one record, read it, close.
func BenchmarkBindingOpenClose(b *testing.B) {
	label := benchLabel(b)
	b.Run(fmt.Sprintf("%s/cold", label), func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			dir := b.TempDir()
			db, err := Open(filepath.Join(dir, "db"), WithLibraryPath(benchLibrary(b)))
			if err != nil {
				b.Fatal(err)
			}
			if err := db.PutBytes([]byte("startup"), []byte("value"), 0); err != nil {
				b.Fatal(err)
			}
			if _, err := db.GetBytes([]byte("startup")); err != nil {
				b.Fatal(err)
			}
			if err := db.Close(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
