package repocheck

import (
	"regexp"
	"strings"
	"testing"
)

// versionedFiles are the non-Go files, besides markdown, that state a tool or
// toolchain version.
var versionedFiles = []string{"Makefile", "Dockerfile", ".github/workflows/ci.yml"}

func textFiles(t *testing.T, extra ...string) []string {
	t.Helper()
	return append(markdownFiles(t), extra...)
}

var goVersionMention = regexp.MustCompile(`\bGo (1\.\d+(?:\.\d+)?)\b|golang:(1\.\d+(?:\.\d+)?)`)

// TestGoVersionAgreesWithGoMod fails when a doc or the Dockerfile names a Go
// version other than the `go` line in go.mod. The builder image runs with
// GOTOOLCHAIN=local, so a stale Dockerfile tag fails the image build.
//
// It reads "Go 1.x" and "golang:1.x" only; a version written another way, such
// as "go1.26", is not seen.
func TestGoVersionAgreesWithGoMod(t *testing.T) {
	t.Parallel()
	m := regexp.MustCompile(`(?m)^go (\S+)`).FindStringSubmatch(readFile(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no `go` line")
	}
	want := m[1]
	for _, f := range textFiles(t, "Dockerfile") {
		eachLine(readFile(t, f), func(n int, line string) {
			for _, mm := range goVersionMention.FindAllStringSubmatch(line, -1) {
				if got := mm[1] + mm[2]; got != want {
					t.Errorf("%s:%d says Go %s but go.mod says %s. Edit %s to %s; go.mod is the source (AGENTS.md: Docs mirror code, Go version).",
						f, n, got, want, f, want)
				}
			}
		})
	}
}

// tool describes where a pinned tool's version is written. Every pattern has
// one capture group holding the version (or "latest").
type tool struct {
	name string
	// inCI finds the version in ci.yml, the source of truth.
	inCI func(ci string) string
	// mention matches a version of the tool anywhere else.
	mention *regexp.Regexp
}

var tools = []tool{
	{
		name:    "golangci-lint",
		inCI:    golangciVersionInCI,
		mention: regexp.MustCompile(`golangci-lint(?:/v2/cmd/golangci-lint)?[@ ](v\d+\.\d+\.\d+|latest)`),
	},
	{
		name: "govulncheck",
		inCI: func(ci string) string {
			if m := regexp.MustCompile(`govulncheck@(v\d+\.\d+\.\d+)`).FindStringSubmatch(ci); m != nil {
				return m[1]
			}
			return ""
		},
		mention: regexp.MustCompile(`govulncheck@(v\d+\.\d+\.\d+|latest)`),
	},
}

// golangciVersionInCI returns the `version:` input of the golangci-lint-action
// step, ignoring other steps' version: lines.
func golangciVersionInCI(ci string) string {
	inStep := false
	version := regexp.MustCompile(`^\s*version:\s*(v\d+\.\d+\.\d+)`)
	for line := range strings.SplitSeq(ci, "\n") {
		switch {
		case strings.Contains(line, "golangci/golangci-lint-action@"):
			inStep = true
		case inStep && strings.HasPrefix(strings.TrimSpace(line), "- "):
			inStep = false
		case inStep:
			if m := version.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// TestToolPinsAgreeWithCI fails when a file pins golangci-lint or govulncheck
// to a version other than the one .github/workflows/ci.yml pins, or leaves
// either on @latest. CI is the source because it is the gate; the @latest rule
// is separate because a file that says @latest never disagrees with a file
// that also says @latest.
//
// It covers only these two tools. The Makefile's go-licenses and godoc installs
// are unpinned and not checked. A pin written in a form the patterns above do
// not match is not seen.
func TestToolPinsAgreeWithCI(t *testing.T) {
	t.Parallel()
	ci := readFile(t, ".github/workflows/ci.yml")
	files := textFiles(t, versionedFiles...)
	for _, tl := range tools {
		want := tl.inCI(ci)
		if want == "" {
			t.Fatalf(".github/workflows/ci.yml pins no %s version the check can read; restore the pin (golangci-lint: `version: vX.Y.Z` under golangci-lint-action; govulncheck: `govulncheck@vX.Y.Z` in the run line)", tl.name)
		}
		for _, f := range files {
			eachLine(readFile(t, f), func(n int, line string) {
				for _, m := range tl.mention.FindAllStringSubmatch(line, -1) {
					switch got := m[1]; {
					case got == "latest":
						t.Errorf("%s:%d installs %s@latest. Pin it to %s, the version in .github/workflows/ci.yml.", f, n, tl.name, want)
					case got != want:
						t.Errorf("%s:%d pins %s %s but .github/workflows/ci.yml pins %s. Edit %s to %s; ci.yml is the source.", f, n, tl.name, got, want, f, want)
					}
				}
			})
		}
	}
}
