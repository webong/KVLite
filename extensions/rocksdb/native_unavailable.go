//go:build !rocksdb

package rocksdb

import "kvlite"

func nativeAvailable() error {
	return kvlite.ErrRocksDBNotBuilt
}

func openNative(_ string, _ kvlite.DriverOptions) (kvlite.Engine, error) {
	return nil, kvlite.ErrRocksDBNotBuilt
}
