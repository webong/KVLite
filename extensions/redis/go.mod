module github.com/webong/kvlite/extensions/redis

go 1.23

require (
	kvlite v0.1.0
	github.com/webong/kvlite/extensions/http v0.1.0
)

replace kvlite => ../../src

replace github.com/webong/kvlite/extensions/http => ../http
