package repocheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var findRoot = sync.OnceValues(func() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fs.ErrNotExist
		}
		dir = parent
	}
})

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := findRoot()
	if err != nil {
		t.Fatalf("no go.mod found at or above the working directory: %v", err)
	}
	return root
}

func readFile(t *testing.T, rel string) string {
	t.Helper()
	//nolint:gosec // rel is a literal path chosen by a check, never input
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("%s is missing or unreadable (%v); a check reads it, so restore the file or update the check in test/repocheck", rel, err)
	}
	return string(b)
}

func relPath(t *testing.T, abs string) string {
	t.Helper()
	rel, err := filepath.Rel(repoRoot(t), abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

func markdownFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			out = append(out, relPath(t, p))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s for markdown files: %v", root, err)
	}
	return out
}

func eachLine(text string, fn func(n int, line string)) {
	for i, line := range strings.Split(text, "\n") {
		fn(i+1, line)
	}
}

type goFile struct {
	path string
	file *ast.File
}

func parseGo(t *testing.T, dirs ...string) []goFile {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	var out []goFile
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			out = append(out, goFile{path: relPath(t, p), file: f})
			return nil
		})
		if err != nil {
			t.Fatalf("parsing Go files under %s: %v", dir, err)
		}
	}
	return out
}

func isTestFile(path string) bool { return strings.HasSuffix(path, "_test.go") }
