package brand

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The product name must live in Name only, and the former names in InstallName and FormerName only. Tests may spell them out to pin
// the copy.
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
		for _, n := range []string{Name, InstallName, FormerName} {
			if strings.Contains(string(b), n) {
				t.Errorf("%s spells out %q; use package brand", path, n)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
