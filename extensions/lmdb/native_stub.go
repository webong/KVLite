//go:build !cgo

package lmdb

import (
	"fmt"

	"kvlite"
)

func nativeAvailable() error                   { return fmt.Errorf("kvlite: LMDB driver requires CGO_ENABLED=1") }
func openNative(string) (kvlite.Engine, error) { return nil, nativeAvailable() }
