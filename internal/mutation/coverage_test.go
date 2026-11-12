package mutation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"testing"
)

var guardComment = regexp.MustCompile(`^Test\w+ (pins|guards|enforces)\b`)

// TestEveryGuardTestHasAMutant fails when a test whose comment says it pins,
// guards or enforces something is named by no mutant. notYetMutated holds the
// guard tests that predate the harness; it may only shrink.
//
// It does not catch a guard test whose comment uses other words, because it
// trusts the comment's wording, and it does not check that the mutant is a good
// one, only that one names the test.
func TestEveryGuardTestHasAMutant(t *testing.T) {
	t.Parallel()
	killed := map[string]bool{}
	for _, m := range mutants {
		for _, k := range m.Kills {
			killed[k] = true
		}
	}
	files, _ := filepath.Glob(filepath.Join(moduleRoot(t), "internal", "*", "*_test.go"))
	seen := map[string]bool{}
	for _, path := range files {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Doc == nil || !guardComment.MatchString(fn.Doc.Text()) {
				continue
			}
			name := fn.Name.Name
			seen[name] = true
			switch {
			case killed[name] && notYetMutated[name]:
				t.Errorf("%s now has a mutant; delete it from notYetMutated", name)
			case !killed[name] && !notYetMutated[name]:
				t.Errorf("%s (%s) is a guard test with no mutant; add one to mutants in internal/mutation/mutants_test.go that it must kill",
					name, filepath.Base(path))
			}
		}
	}
	for name := range notYetMutated {
		if !seen[name] {
			t.Errorf("%s is in notYetMutated but is no longer a guard test; delete it", name)
		}
	}
}
