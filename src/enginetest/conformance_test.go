package enginetest

import (
	"testing"

	"kvlite"
)

func TestMemoryAtomicMutations(t *testing.T) {
	RunAtomicMutations(t, kvlite.DriverMemory)
}
