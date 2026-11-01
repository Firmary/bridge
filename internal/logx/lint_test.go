package logx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestOnlyLogxLogs is the never-log lint: outside cmd/, production code may not write to stdout or stderr, may not
// use the standard log packages, and may not mint log event names from strings. cmd/ may print to the terminal for
// the person running it (for example the pairing code), but still may not use the log packages.
func TestOnlyLogxLogs(t *testing.T) {
	root, _ := filepath.Abs("../..")
	fset := token.NewFileSet()
	var problems []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		inCmd := strings.HasPrefix(rel, "cmd/")
		inLogx := strings.HasPrefix(rel, "internal/logx/")
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "log" || p == "log/slog" || p == "log/syslog" {
				problems = append(problems, rel+": imports "+p)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				id, ok := x.X.(*ast.Ident)
				if !ok {
					return true
				}
				name := id.Name + "." + x.Sel.Name
				switch {
				case !inCmd && (name == "os.Stdout" || name == "os.Stderr"):
					problems = append(problems, rel+": uses "+name)
				case !inCmd && id.Name == "fmt" && strings.HasPrefix(x.Sel.Name, "Print"):
					problems = append(problems, rel+": uses "+name)
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && !inLogx && sel.Sel.Name == "Event" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "logx" {
						problems = append(problems, rel+": builds a logx.Event from a value; use the constants")
					}
				}
				if id, ok := x.Fun.(*ast.Ident); ok && !inCmd && (id.Name == "println" || id.Name == "print") {
					problems = append(problems, rel+": uses builtin "+id.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}
