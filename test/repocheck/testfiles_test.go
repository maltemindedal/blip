package repocheck

import (
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
