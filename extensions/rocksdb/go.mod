module github.com/webong/kvlite/extensions/rocksdb

go 1.23

require (
	github.com/linxGnu/grocksdb v1.10.6
	kvlite v0.1.0
)

replace kvlite => ../../src
