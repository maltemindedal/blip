package repocheck

import (
	"go/ast"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// docExamples are identifiers the docs use as placeholders, not as references.
var docExamples = []string{"TestSubjectBehavior"}

// TestDocIdentifiersExist fails when a doc names a test, benchmark, helper or
// call in backticks that no .go file declares. It catches a rename or deletion
// the docs did not follow.
//
// It sees functions and methods only, and only names that start with Test or
// Benchmark, follow `testhelpers.`, or end in `(`. A type, constant or field
// named in prose is not checked.
func TestDocIdentifiersExist(t *testing.T) {
	t.Parallel()
	declared := map[string]bool{}
	for _, g := range parseGo(t, ".") {
		for _, d := range g.file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				declared[fd.Name.Name] = true
			}
		}
	}
	ref := regexp.MustCompile("`((?:Test|Benchmark)[A-Z]\\w*|testhelpers\\.[A-Z]\\w*|[A-Z]\\w*\\()")
	for _, f := range markdownFiles(t) {
		eachLine(readFile(t, f), func(n int, line string) {
			for _, m := range ref.FindAllStringSubmatch(line, -1) {
				name := strings.TrimSuffix(m[1], "(")
				short := name[strings.LastIndex(name, ".")+1:]
				if !declared[short] && !slices.Contains(docExamples, short) {
					t.Errorf("%s:%d names `%s`, which no .go file declares. Edit %s to the identifier's current name, or delete the sentence.", f, n, name, f)
				}
			}
		})
	}
}

type logCall struct {
	msg   string
	level string
	file  string
}

var slogLevels = []string{"Debug", "Info", "Warn", "Error"}

// codeLogCalls returns the constant first argument of every Debug, Info, Warn
// or Error call in non-test sources under internal and cmd. A message built at
// run time (positiveIntFromEnv's "invalid "+name) is not a constant and is
// skipped.
func codeLogCalls(t *testing.T) []logCall {
	t.Helper()
	var out []logCall
	for _, g := range parseGo(t, "internal", "cmd") {
		if isTestFile(g.path) {
			continue
		}
		ast.Inspect(g.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !slices.Contains(slogLevels, sel.Sel.Name) {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				msg, err := strconv.Unquote(lit.Value)
				if err == nil {
					out = append(out, logCall{msg: msg, level: strings.ToUpper(sel.Sel.Name), file: g.path})
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("found no slog calls under internal/ or cmd/; the extractor no longer matches the code")
	}
	return out
}

var alertRow = regexp.MustCompile("^\\| `([^`]+)`\\s*\\| (DEBUG|INFO|WARN|ERROR)\\b")

// TestAlertTableMatchesLogCalls fails when a row of the alert table in
// docs/guides/deploying-to-production.md names a msg and level that no slog
// call logs. The msg strings are an operator contract: alerts match them
// literally. A trailing "..." in the table means "starts with".
//
// It does not check that every logged msg has a row, or the Meaning column.
func TestAlertTableMatchesLogCalls(t *testing.T) {
	t.Parallel()
	const doc = "docs/guides/deploying-to-production.md"
	calls := codeLogCalls(t)
	rows := 0
	eachLine(readFile(t, doc), func(n int, line string) {
		m := alertRow.FindStringSubmatch(line)
		if m == nil {
			return
		}
		rows++
		prefix, isPrefix := strings.CutSuffix(m[1], "...")
		var gotLevel string
		found := slices.ContainsFunc(calls, func(c logCall) bool {
			if c.msg == m[1] || (isPrefix && strings.HasPrefix(c.msg, prefix)) {
				gotLevel = c.level
				return true
			}
			return false
		})
		switch {
		case !found:
			t.Errorf("%s:%d alerts on msg %q, which no slog call logs. Copy the exact msg string from the code into the table, or delete the row.", doc, n, m[1])
		case gotLevel != m[2]:
			t.Errorf("%s:%d says msg %q is %s, but the code logs it at %s. Edit the Level cell.", doc, n, m[1], m[2], gotLevel)
		}
	})
	if rows == 0 {
		t.Fatalf("%s has no alert table rows; the check reads rows shaped like | `msg` | LEVEL |", doc)
	}
}

// TestDocsQuoteLogMsgsExactly fails when a doc quotes a log msg with different
// capitalization than the code. Operators grep for the exact string.
//
// It catches case differences only, not a reworded message.
func TestDocsQuoteLogMsgsExactly(t *testing.T) {
	t.Parallel()
	calls := codeLogCalls(t)
	span := regexp.MustCompile("`([^`\n]+)`")
	for _, f := range markdownFiles(t) {
		eachLine(readFile(t, f), func(n int, line string) {
			for _, m := range span.FindAllStringSubmatch(line, -1) {
				for _, c := range calls {
					if m[1] != c.msg && strings.EqualFold(m[1], c.msg) {
						t.Errorf("%s:%d quotes log msg `%s`; %s logs %q. Edit %s to copy it exactly.", f, n, m[1], c.file, c.msg, f)
					}
				}
			}
		})
	}
}
