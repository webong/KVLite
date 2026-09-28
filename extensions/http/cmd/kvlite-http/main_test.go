package main

import (
	"testing"

	"kvlite"
)

func TestAddDriverPath(t *testing.T) {
	paths := make(map[kvlite.DriverName]string)
	if err := addDriverPath(paths, "LevelDB = ./level-data"); err != nil {
		t.Fatal(err)
	}
	if got := paths[kvlite.DriverLevelDB]; got != "./level-data" {
		t.Fatalf("leveldb path = %q, want ./level-data", got)
	}
	for _, raw := range []string{"LevelDB=./other", "invalid", "=./data", "rocksdb="} {
		if err := addDriverPath(paths, raw); err == nil {
			t.Fatalf("addDriverPath(%q) succeeded, want error", raw)
		}
	}
}
