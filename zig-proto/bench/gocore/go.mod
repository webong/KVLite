module kvlite-zig-proto/gocorebench

go 1.23.0

require (
	github.com/webong/kvlite/extensions/lmdb v0.1.0
	kvlite v0.1.0
)

require github.com/PowerDNS/lmdb-go v1.9.4 // indirect

// Run with GOWORK=off so this comparison module stays independent of the
// repository workspace.
replace kvlite => ../../../src

replace github.com/webong/kvlite/extensions/lmdb v0.1.0 => ../../../extensions/lmdb
