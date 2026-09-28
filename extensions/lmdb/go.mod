module github.com/webong/kvlite/extensions/lmdb

go 1.23

require (
	github.com/PowerDNS/lmdb-go v1.9.4
	kvlite v0.1.0
)

replace kvlite => ../../src
