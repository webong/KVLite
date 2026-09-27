package enginetest

import (
	"testing"

	"github.com/webong/kvlite"
)

func TestMemoryAtomicMutations(t *testing.T) {
	RunAtomicMutations(t, kvlite.DriverMemory)
}
