package repocheck

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	documentedTarget = regexp.MustCompile(`^## ([A-Za-z0-9_.-]+):`)
	ruleLine         = regexp.MustCompile(`^([A-Za-z0-9_.-]+)\s*:([^=]|$)`)
)

func documentedTargets(t *testing.T) []string {
	t.Helper()
	var names []string
	eachLine(readFile(t, "Makefile"), func(_ int, line string) {
		if m := documentedTarget.FindStringSubmatch(line); m != nil {
			names = append(names, m[1])
		}
	})
	if len(names) == 0 {
		t.Fatal("Makefile has no `## target: description` lines; `make help` and this check both read them")
	}
	return names
}

// TestMakefileDocumentedTargetsHaveRules fails when a `## name:` help comment
// is not followed by the `name:` rule line. Deleting only the rule line leaves
// its recipe attached to the previous target, which make accepts silently.
//
// It does not check that a rule's recipe works; the dry run below does that.
func TestMakefileDocumentedTargetsHaveRules(t *testing.T) {
	t.Parallel()
	lines := strings.Split(readFile(t, "Makefile"), "\n")
	for i, line := range lines {
		m := documentedTarget.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		j := i + 1
		for j < len(lines) && (strings.HasPrefix(lines[j], "#") || strings.TrimSpace(lines[j]) == "") {
			j++
		}
		got := ""
		if j < len(lines) {
			if r := ruleLine.FindStringSubmatch(lines[j]); r != nil {
				got = r[1]
			}
		}
		if got != m[1] {
			t.Errorf("Makefile:%d documents target %q but the next rule line is %q (line %d). "+
				"Restore the `%s: <prerequisites>` line directly above its recipe in Makefile; "+
				"without it the recipe runs inside the previous target.",
				i+1, m[1], strings.TrimSpace(lines[min(j, len(lines)-1)]), j+1, m[1])
		}
	}
}

// TestMakefileDryRunsEveryDocumentedTarget runs `make -n <target>` for each
// documented target, so an undefined prerequisite or a syntax error in the
// Makefile fails here instead of when someone first runs the target. A dry run
// prints recipes without executing them, so it needs no network and does not
// run `clean`.
//
// It does not run any recipe, so it cannot see a recipe that fails when
// executed. It is skipped, not failed, where make is not installed.
func TestMakefileDryRunsEveryDocumentedTarget(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH; Makefile targets were not dry-run")
	}
	root := repoRoot(t)
	for _, name := range documentedTargets(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			//nolint:gosec // name comes from the repository's own Makefile
			cmd := exec.CommandContext(ctx, "make", "-n", name)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("`make -n %s` failed (%v): %s\nFix the rule or prerequisite in Makefile, or delete the `## %s:` help line if the target is gone.",
					name, err, lastLine(out), name)
			}
		})
	}
}

func lastLine(out []byte) string {
	s := strings.TrimSpace(string(out))
	return s[strings.LastIndex(s, "\n")+1:]
}
