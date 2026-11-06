package proc

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

// TestOnlyProcStarts is the no-window lint: outside this package, production code starts child processes only through
// proc.Command and proc.CommandContext, never exec.Command, exec.CommandContext, os.StartProcess or the syscall and
// x/sys/windows process calls, so no new call site can flash a console window on Windows. Those packages may not be
// dot-imported, which would hide the calls from this check.
func TestOnlyProcStarts(t *testing.T) {
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
		if strings.HasPrefix(rel, "internal/proc/") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		banned := map[string]map[string]bool{} // local package name -> banned functions
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			fns := map[string]map[string]bool{
				"os/exec":                  {"Command": true, "CommandContext": true},
				"os":                       {"StartProcess": true},
				"syscall":                  {"StartProcess": true, "ForkExec": true, "CreateProcess": true},
				"golang.org/x/sys/windows": {"CreateProcess": true, "CreateProcessAsUser": true},
			}[p]
			if fns == nil {
				continue
			}
			if imp.Name != nil && imp.Name.Name == "." {
				problems = append(problems, rel+": dot-imports "+p)
				continue
			}
			name := p[strings.LastIndex(p, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			banned[name] = fns
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && banned[id.Name][sel.Sel.Name] {
				problems = append(problems, rel+":"+strconv.Itoa(fset.Position(sel.Pos()).Line)+": uses "+id.Name+"."+sel.Sel.Name+"; use proc.Command or proc.CommandContext")
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
