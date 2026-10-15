//go:build !cgo || (!linux && !darwin && !windows)

package kvlite

import "fmt"

func openNative(_, _, _ string, _ bool) (nativeDB, error) {
	return nil, fmt.Errorf("%w: the embedded Go binding needs cgo with a gcc-compatible compiler on Linux, macOS, or Windows; use the KVLite CLI or an optional transport on this target", ErrNativeLibrary)
}
