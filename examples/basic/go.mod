module github.com/webong/kvlite/examples/basic

go 1.23

require (
	kvlite v0.1.0
	github.com/webong/kvlite/extensions/rocksdb v0.1.0
)

require github.com/linxGnu/grocksdb v1.10.6 // indirect

replace kvlite => ../../src

replace github.com/webong/kvlite/extensions/rocksdb => ../../extensions/rocksdb
