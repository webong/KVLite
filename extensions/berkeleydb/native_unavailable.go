//go:build !berkeleydb || !cgo

package berkeleydb

import "kvlite"

func nativeAvailable() error {
	return kvlite.ErrBerkeleyDBNotBuilt
}

func openNative(_ string, _ kvlite.DriverOptions) (kvlite.Engine, error) {
	return nil, kvlite.ErrBerkeleyDBNotBuilt
}
