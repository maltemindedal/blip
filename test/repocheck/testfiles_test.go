package repocheck

import (
	"go/ast"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// TestInternalTestFilesNameASourceFile fails for a test file under internal/
// whose name matches no source file in its directory: `x_test.go` and
// `x_internal_test.go` need an `x.go` beside them (AGENTS.md: Tests).
//
// A test-only package, such as internal/mutation, has no source file to name
// and is skipped. It checks the name only, not that the tests in the file
// cover x.go.
func TestInternalTestFilesNameASourceFile(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	files := parseGo(t, "internal")
	hasSource := map[string]bool{}
	for _, g := range files {
		if !isTestFile(g.path) {
			hasSource[path.Dir(g.path)] = true
		}
	}
	checked := 0
	for _, g := range files {
		if !isTestFile(g.path) || !hasSource[path.Dir(g.path)] {
			continue
		}
		checked++
		base := strings.TrimSuffix(strings.TrimSuffix(path.Base(g.path), "_test.go"), "_internal")
		src := path.Join(path.Dir(g.path), base+".go")
		if _, err := os.Stat(filepath.Join(root, src)); err != nil {
			t.Errorf("%s covers no %s. Move its tests into the _internal_test.go file named after the source file they cover, or rename it to match one.", g.path, src)
		}
	}
	if checked == 0 {
		t.Fatal("found no test files under internal/; the check no longer matches the layout")
	}
}

// TestTesthelpersExportsHaveCallers fails for an exported function in
// test/testhelpers that no test outside that package calls. The linter's
// `unused` check cannot see it: an exported name is always "used".
//
// It looks for `testhelpers.Name` selectors only, so a call through a
// dot-import or an alias is missed, and it checks functions, not types or
// constants. It is skipped when test/testhelpers does not exist.
func TestTesthelpersExportsHaveCallers(t *testing.T) {
	t.Parallel()
	const dir = "test/testhelpers"
	if _, err := os.Stat(filepath.Join(repoRoot(t), dir)); err != nil {
		t.Skipf("%s does not exist; no exported test helpers to check", dir)
	}
	exported := map[string]string{}
	used := map[string]bool{}
	for _, g := range parseGo(t, "test", "internal", "cmd") {
		if path.Dir(g.path) == dir {
			for _, d := range g.file.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.IsExported() {
					exported[fd.Name.Name] = g.path
				}
			}
			continue
		}
		ast.Inspect(g.file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "testhelpers" {
					used[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	for name, file := range exported {
		if !used[name] {
			t.Errorf("%s exports %s, which no test outside %s calls. Delete it, or unexport it if another helper there uses it.", file, name, dir)
		}
	}
}
