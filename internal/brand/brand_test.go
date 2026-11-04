package brand

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The product name is not final, so it must live in Name only. Tests may spell it out to pin the copy.
func TestNameIsOnlyInThisFile(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			filepath.Base(path) == "brand.go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), Name) {
			t.Errorf("%s spells out %q; use brand.Name", path, Name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
