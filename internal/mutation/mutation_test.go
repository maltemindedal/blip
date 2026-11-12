// Package mutation checks that the module's guard tests guard something. It
// replays past bugs as mutants and fails when the test meant to catch one
// passes. It contains only tests, so `go test ./internal/...` runs it.
package mutation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// edit replaces Old with New in File, a path relative to the module root. An
// empty Old adds File as a new file with the content New.
type edit struct {
	File string
	Old  string
	New  string
}

// mutant is one past mistake replayed against the current code.
type mutant struct {
	Name   string
	Origin string   // the commit that introduced the bug or added the test that catches it
	Edits  []edit   // applied in order to a copy of the module
	Kills  []string // top-level tests that must fail under the mutant
}

// moduleRoot finds the directory holding go.mod by walking up from the test's
// working directory, which `go test` sets to the package directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// apply returns the content of every file the mutant touches, keyed by path.
func apply(root string, m mutant) (map[string]string, error) {
	files := map[string]string{}
	for _, e := range m.Edits {
		if strings.HasSuffix(e.File, "_test.go") {
			return nil, fmt.Errorf("%s: a mutant edits production code, never a test", e.File)
		}
		src, seen := files[e.File]
		if !seen && e.Old != "" {
			b, err := os.ReadFile(filepath.Join(root, e.File)) //nolint:gosec // the path comes from the mutants table
			if err != nil {
				return nil, err
			}
			src = string(b)
		}
		if e.Old == "" {
			if _, err := os.Stat(filepath.Join(root, e.File)); err == nil || seen {
				return nil, fmt.Errorf("%s: an edit with no Old text adds a new file, and this one exists", e.File)
			}
			files[e.File] = e.New
			continue
		}
		if n := strings.Count(src, e.Old); n != 1 {
			return nil, fmt.Errorf("%s: Old text occurs %d times, want exactly 1; move the mutant with the code it guards", e.File, n)
		}
		files[e.File] = strings.Replace(src, e.Old, e.New, 1)
	}
	return files, nil
}

// copyModule copies the parts of the module a unit test needs into dst. It
// skips this package, which would otherwise run inside its own copy.
func copyModule(root, dst string) error {
	for _, top := range []string{"go.mod", "go.sum", "cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			switch {
			case rel == filepath.Join("internal", "mutation"):
				return fs.SkipDir
			case d.IsDir():
				return os.MkdirAll(filepath.Join(dst, rel), 0o750)
			case !d.Type().IsRegular():
				return nil
			}
			b, err := os.ReadFile(p) //nolint:gosec // p comes from walking the module root
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dst, rel), b, 0o600) //nolint:gosec // rel comes from walking the module root
		})
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// slots bounds how many mutant builds run at once, so this package does not
// starve the packages `go test` runs beside it.
var slots = make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/2))

type testEvent struct {
	Action string
	Test   string
	Output string
}

// runMutant runs only the mutant's Kills in a copy of the module that carries
// the edits, and returns the outcome of each top-level test by name along with
// the raw output.
//
// It reads the copy from disk, not through `go test -overlay`: a guard such as
// TestClockSeamIsTestOnly parses the package's source files, and an overlay
// never shows it the mutant. The child inherits the environment, so GOFLAGS and
// the build cache apply, but not the outer run's flags. It runs without -race,
// so a mutant that only the race detector kills cannot be expressed here.
func runMutant(t *testing.T, root string, m mutant, files map[string]string) (map[string]string, string, bool) {
	t.Helper()
	dst := t.TempDir()
	if err := copyModule(root, dst); err != nil {
		t.Fatal(err)
	}
	pkgSet := map[string]bool{}
	for rel, content := range files {
		path := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		pkgSet["./"+filepath.ToSlash(filepath.Dir(rel))] = true
	}
	args := []string{"test", "-json", "-count=1", "-vet=off", "-timeout=60s",
		"-run", "^(" + strings.Join(m.Kills, "|") + ")$"}
	pkgs := make([]string, 0, len(pkgSet))
	for p := range pkgSet {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	args = append(args, pkgs...)

	slots <- struct{}{}
	defer func() { <-slots }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...) //nolint:gosec // args are built from the mutants table
	cmd.Dir = dst
	cmd.WaitDelay = 10 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	_ = cmd.Run()
	t.Logf("%s: go %s (%s)", m.Origin, strings.Join(args, " "), time.Since(start).Round(time.Millisecond))

	results := map[string]string{}
	buildFailed := false
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	for dec.More() {
		var ev testEvent
		if err := dec.Decode(&ev); err != nil {
			break
		}
		switch {
		case ev.Action == "build-fail", strings.Contains(ev.Output, "[build failed]"), strings.Contains(ev.Output, "[setup failed]"):
			buildFailed = true
		case ev.Test != "" && (ev.Action == "pass" || ev.Action == "fail" || ev.Action == "skip"):
			results[ev.Test] = ev.Action
		}
	}
	return results, out.String(), buildFailed
}

// TestGuardTestsKillTheirMutants fails when a guard test passes under the bug
// it exists to catch, when a named test does not run, when a mutant no longer
// applies, or when a mutant does not compile.
//
// It proves the listed bugs are caught, not that a guard test catches every bug
// of its shape. It runs without -race and skips under -short.
func TestGuardTestsKillTheirMutants(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped under -short: runs go test once per mutant; run without -short to check the guard tests")
	}
	t.Parallel()
	root := moduleRoot(t)
	for _, m := range mutants {
		t.Run(m.Name, func(t *testing.T) {
			t.Parallel()
			if len(m.Kills) == 0 {
				t.Fatal("mutant names no test to kill")
			}
			files, err := apply(root, m)
			if err != nil {
				t.Fatalf("mutant does not apply: %v", err)
			}
			results, output, buildFailed := runMutant(t, root, m, files)
			if buildFailed {
				t.Fatalf("mutant does not compile, so it proves nothing:\n%s", output)
			}
			for _, name := range m.Kills {
				switch results[name] {
				case "fail":
				case "pass":
					t.Errorf("SURVIVED: %s passes under this mutant (%s)", name, m.Origin)
				default:
					t.Errorf("%s did not run (renamed or deleted?); output:\n%s", name, output)
				}
			}
		})
	}
}
