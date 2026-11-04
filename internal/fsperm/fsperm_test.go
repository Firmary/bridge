//go:build !windows

package fsperm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckOwned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwned(dir, 0o022); err != nil {
		t.Fatalf("private dir refused: %v", err)
	}
	os.Chmod(dir, 0o777)
	if CheckOwned(dir, 0o022) == nil {
		t.Error("world-writable dir accepted")
	}
	if os.Getuid() != 0 {
		if CheckOwned("/", 0o022) == nil {
			t.Error("a directory owned by root accepted")
		}
	}
}
