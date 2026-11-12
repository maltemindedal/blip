package repocheck

import (
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var envName = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+$|^[A-Z]{2,}$`)

func envVarsInCode(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, g := range parseGo(t, "internal", "cmd") {
		if isTestFile(g.path) {
			continue
		}
		ast.Inspect(g.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var fn string
			switch f := call.Fun.(type) {
			case *ast.Ident:
				fn = f.Name
			case *ast.SelectorExpr:
				fn = f.Sel.Name
			case *ast.IndexExpr:
				if id, ok := f.X.(*ast.Ident); ok {
					fn = id.Name
				}
			}
			if !strings.Contains(strings.ToLower(fn), "env") {
				return true
			}
			for _, arg := range call.Args {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil && envName.MatchString(s) {
						out[s] = g.path
					}
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("found no environment variable reads under internal/ or cmd/; the extractor no longer matches the code")
	}
	return out
}

// TestEnvVarsAreMirrored fails when an environment variable the code reads is
// missing from the configuration reference, .env.example, docker-compose.yml or
// the internal test file beside the source file that reads it, or when one of the first three names a variable the
// code does not read.
//
// It finds reads with a string literal in a call whose name contains "env". A
// variable read through a constant or built from parts is not seen, and it
// checks that each file mentions a variable, not that the default matches.
func TestEnvVarsAreMirrored(t *testing.T) {
	t.Parallel()
	code := envVarsInCode(t)

	mirrors := []struct {
		file, hint string
		line       *regexp.Regexp
	}{
		{"docs/reference/configuration.md", "a row in the Variables table", regexp.MustCompile("(?m)^\\| `([A-Z][A-Z0-9_]+)`")},
		{".env.example", "a `NAME=value` line", regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]+)=`)},
		{"docker-compose.yml", "an `environment:` entry", regexp.MustCompile(`(?m)^\s*- ([A-Z][A-Z0-9_]+)=`)},
	}
	for _, mir := range mirrors {
		text := readFile(t, mir.file)
		listed := map[string]bool{}
		for _, m := range mir.line.FindAllStringSubmatch(text, -1) {
			listed[m[1]] = true
		}
		for name, src := range code {
			if !listed[name] {
				t.Errorf("%s reads %s, but %s lacks %s for it. Add one.", src, name, mir.file, mir.hint)
			}
		}
		for name := range listed {
			if _, ok := code[name]; !ok {
				t.Errorf("%s lists %s, which no code under internal/ or cmd/ reads. Delete it, or restore the read.", mir.file, name)
			}
		}
	}

	for name, src := range code {
		testFile := strings.TrimSuffix(src, ".go") + "_internal_test.go"
		if !strings.Contains(readFile(t, testFile), strconv.Quote(name)) {
			t.Errorf("%s reads %s, but %s has no test case that sets it. Add a table-driven test that calls t.Setenv(%q, ...) and checks the reader.", src, name, testFile, name)
		}
	}
}
