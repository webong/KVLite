//go:build !cgo || (!linux && !darwin)

package kvlite

import "fmt"

func openNative(_, _, _ string, _ bool) (nativeDB, error) {
	return nil, fmt.Errorf("%w: embedded Go binding requires cgo on Linux or macOS; use the KVLite CLI or an optional transport on this target", ErrNativeLibrary)
}
