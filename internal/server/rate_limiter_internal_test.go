// Package server: this file holds the rate limiter's tests. Its behaviour —
// refill, the cap at capacity, a frozen clock and a rewound one — is pinned
// through the allowAt clock seam, and TestClockSeamIsTestOnly guards that seam:
// the one check in the package about the package's own shape rather than its
// behaviour.
package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seamFuncs are the clock-injecting entry points on the rate limiter, listed
// here for detection.
//
// Detection matches the identifier alone, deliberately unlike [isSeamWrapper]
// below, which checks the receiver too. Each is loose in its safe direction: a
// name matched too widely here costs a false failure, which a reader sees and
// can rule out in a moment, whereas a wrapper exempted too widely would wave a
// real caller through in silence.
var seamFuncs = map[string]struct{}{
	"allowAt":          {},
	"newRateLimiterAt": {},
}

// TestClockSeamIsTestOnly enforces what the doc comment on [rateLimiter.allowAt]
// promises: outside _test.go files, the seam is named only by its own two
// wrappers. Both seam functions are package-scoped, so nothing else stops a file
// in package server from handing the limiter an instant — and an instant a
// caller chooses is a throttle a caller can loosen. Why the seam is shaped this
// way is in docs/architecture/overview.md; this is the check that keeps it that
// way.
//
// It parses rather than greps, so a mention in a comment or a string does not
// fail it, and a new production file is covered the day it is added. It walks
// every top-level declaration rather than function bodies alone, so a
// package-level var calling the seam is caught as readily as a call inside a
// function.
//
// Scope, stated plainly: this guards the seam functions. It is not a guarantee
// that no production code can arrange a stale baseline by other means — the
// rateLimiter struct and its fields are package-scoped too, so a composite
// literal or a direct write to last would sidestep this check. That door is
// older than the seam and unchanged by it; closing it is a separate job.
func TestClockSeamIsTestOnly(t *testing.T) {
	t.Parallel()

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("failed to list package sources: %v", err)
	}

	fset := token.NewFileSet()

	var checked int

	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		checked++

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			checkDeclForSeam(t, fset, decl)
		}
	}

	// A glob that quietly matched nothing would make this test vacuous.
	if checked == 0 {
		t.Fatal("found no non-test sources to check; the seam is unguarded")
	}
}

// checkDeclForSeam reports every mention of the seam within one top-level
// declaration, naming the declaration it was found in so a failure says where to
// look without opening the file.
//
// Two kinds of declaration are handled specially: the wrappers exist precisely
// to call the seam, so they are skipped whole, and the seam's own declarations
// are walked everywhere except their names, since declaring a function is not
// calling it.
func checkDeclForSeam(t *testing.T, fset *token.FileSet, decl ast.Decl) {
	t.Helper()

	fn, isFunc := decl.(*ast.FuncDecl)
	if !isFunc {
		checkNodeForSeam(t, fset, "package-level declaration", decl)
		return
	}

	if isSeamWrapper(fn) {
		return
	}

	where := funcLabel(fn)

	if _, isSeam := seamFuncs[fn.Name.Name]; isSeam {
		checkNodeForSeam(t, fset, where, fn.Type)
		if fn.Body != nil {
			checkNodeForSeam(t, fset, where, fn.Body)
		}
		return
	}

	checkNodeForSeam(t, fset, where, decl)
}

// checkNodeForSeam fails the test once per identifier under node that names the
// seam.
func checkNodeForSeam(t *testing.T, fset *token.FileSet, where string, node ast.Node) {
	t.Helper()

	ast.Inspect(node, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}

		if _, isSeam := seamFuncs[ident.Name]; isSeam {
			t.Errorf("%s: %s names the clock seam %s; production must go through the "+
				"no-argument wrapper so the throttle stays the configured one",
				fset.Position(ident.Pos()), where, ident.Name)
		}

		return true
	})
}

// isSeamWrapper reports whether fn is one of the two functions allowed to name
// the seam: the rateLimiter.allow method, and the package-level newRateLimiter.
// The receiver is checked rather than the name alone, so an unrelated method
// that happens to be called allow does not inherit the exemption.
func isSeamWrapper(fn *ast.FuncDecl) bool {
	switch fn.Name.Name {
	case "newRateLimiter":
		return fn.Recv == nil
	case "allow":
		return receiverTypeName(fn) == "rateLimiter"
	default:
		return false
	}
}

// funcLabel names fn the way a reader would go looking for it: "func allow" for
// a plain function, "func (rateLimiter) allow" for a method.
func funcLabel(fn *ast.FuncDecl) string {
	if recv := receiverTypeName(fn); recv != "" {
		return "func (" + recv + ") " + fn.Name.Name
	}

	return "func " + fn.Name.Name
}

// receiverTypeName returns the name of fn's receiver type, without any pointer,
// or "" when fn is not a method.
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}

	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}

	ident, ok := expr.(*ast.Ident)
	if !ok {
		return ""
	}

	return ident.Name
}

// rateLimiterEpoch is an arbitrary fixed instant. Every refill test below goes
// through the allowAt seam so it can advance the clock by hand and pin refill
// by arithmetic instead of by sleeping.
var rateLimiterEpoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// drainRateLimiter spends a full bucket and asserts the next message is refused,
// leaving the limiter empty.
//
// How the limiter is reached is the caller's to supply, because the tests below
// reach it two ways: the refill tests spend through the seam at a fixed instant,
// via [attemptAt], while the one test that drives the production path passes
// allow itself.
func drainRateLimiter(t *testing.T, capacity int, attempt func() bool) {
	t.Helper()

	for i := range capacity {
		if !attempt() {
			t.Fatalf("burst token %d was denied", i)
		}
	}

	if attempt() {
		t.Fatal("limiter allowed a message past its burst")
	}
}

// attemptAt is the attempt [drainRateLimiter] needs to spend rl through the seam
// at a fixed instant, leaving its baseline at now.
func attemptAt(rl *rateLimiter, now time.Time) func() bool {
	return func() bool { return rl.allowAt(now) }
}

// allowedAt reports how many messages the limiter permits at a single instant,
// stopping at the first refusal and giving up after limit calls.
func allowedAt(rl *rateLimiter, now time.Time, limit int) int {
	for i := range limit {
		if !rl.allowAt(now) {
			return i
		}
	}

	return limit
}

// TestZeroValueRateLimiterAllows pins the zero value as unlimited. A Client
// assembled without NewClient must not be silently throttled to nothing.
func TestZeroValueRateLimiterAllows(t *testing.T) {
	t.Parallel()

	c := &Client{}
	for i := range 100 {
		if !c.rateLimiter.allow() {
			t.Fatalf("zero-value limiter denied message %d", i)
		}
	}
}

// TestRateLimiterThrottlesAtCapacity checks the configured limiter still
// throttles, so the zero-value escape hatch has not disabled the real path.
//
// It reaches the limiter the way the read pump does, through the no-argument
// entry points, which is what it is for: those read the clock themselves, so an
// hour-long refill interval cannot hand a token back mid-test and the burst is
// the whole budget. The refill arithmetic is pinned below, against the allowAt
// seam.
//
// What it does not do is check the constructor's baseline against allow's
// clock, in either direction. A baseline behind the clock is absorbed by the cap
// at capacity, so a bucket that starts full arrives full anyway; one ahead of it
// yields negative elapsed time, which allowAt skips — and over the microseconds
// this test runs, neither shows up as a token granted or withheld. The mutants
// were tried and survived.
func TestRateLimiterThrottlesAtCapacity(t *testing.T) {
	t.Parallel()

	const capacity = 3
	rl := newRateLimiter(capacity, time.Hour)

	drainRateLimiter(t, capacity, rl.allow)
}

// TestRateLimiterRefillsFromElapsedTime pins partial refill. Four tokens per
// second means 600ms is worth 2.4 of them, and the 0.4 left over must carry:
// the following 200ms is worth only 0.8 on its own, so the message it lets
// through is proof the residue was kept rather than rounded away.
func TestRateLimiterRefillsFromElapsedTime(t *testing.T) {
	t.Parallel()

	const capacity = 4
	rl := newRateLimiterAt(capacity, time.Second, rateLimiterEpoch)
	drainRateLimiter(t, capacity, attemptAt(&rl, rateLimiterEpoch))

	if n := allowedAt(&rl, rateLimiterEpoch.Add(600*time.Millisecond), capacity); n != 2 {
		t.Fatalf("600ms of refill allowed %d messages, want 2", n)
	}

	if n := allowedAt(&rl, rateLimiterEpoch.Add(800*time.Millisecond), capacity); n != 1 {
		t.Fatalf("a further 200ms of refill allowed %d messages, want 1", n)
	}
}

// TestRateLimiterRestoresBurstAfterOneInterval pins the headline promise: one
// interval after the bucket ran dry, the whole burst is back and no more.
func TestRateLimiterRestoresBurstAfterOneInterval(t *testing.T) {
	t.Parallel()

	const (
		capacity = 3
		interval = 500 * time.Millisecond
	)

	rl := newRateLimiterAt(capacity, interval, rateLimiterEpoch)
	drainRateLimiter(t, capacity, attemptAt(&rl, rateLimiterEpoch))

	if n := allowedAt(&rl, rateLimiterEpoch.Add(interval), capacity+1); n != capacity {
		t.Fatalf("one interval restored %d messages, want %d", n, capacity)
	}
}

// TestRateLimiterCapsRefillAtCapacity pins that idling banks nothing: however
// long a connection stays quiet it comes back with one burst, not a backlog.
func TestRateLimiterCapsRefillAtCapacity(t *testing.T) {
	t.Parallel()

	const (
		capacity = 3
		interval = time.Second
	)

	rl := newRateLimiterAt(capacity, interval, rateLimiterEpoch)
	drainRateLimiter(t, capacity, attemptAt(&rl, rateLimiterEpoch))

	if n := allowedAt(&rl, rateLimiterEpoch.Add(100*interval), capacity*10); n != capacity {
		t.Fatalf("100 idle intervals allowed %d messages, want %d", n, capacity)
	}
}

// TestRateLimiterGrantsNothingWithoutElapsedTime pins refill as a function of
// the clock and nothing else: repeated calls at one instant never restore a
// token, however many of them there are.
func TestRateLimiterGrantsNothingWithoutElapsedTime(t *testing.T) {
	t.Parallel()

	const capacity = 2
	rl := newRateLimiterAt(capacity, time.Second, rateLimiterEpoch)
	drainRateLimiter(t, capacity, attemptAt(&rl, rateLimiterEpoch))

	for i := range 10 {
		if rl.allowAt(rateLimiterEpoch) {
			t.Fatalf("a frozen clock refilled a token at call %d", i)
		}
	}
}

// TestRateLimiterIgnoresBackwardsClock pins the guard on non-positive elapsed
// time. Negative elapsed time must be skipped rather than folded into the
// arithmetic, where it would subtract tokens the connection had already earned.
func TestRateLimiterIgnoresBackwardsClock(t *testing.T) {
	t.Parallel()

	const capacity = 2
	past := rateLimiterEpoch.Add(-time.Hour)

	// A rewound clock neither grants tokens nor destroys them: the full burst is
	// still spendable, and it is still only a burst.
	rl := newRateLimiterAt(capacity, time.Second, rateLimiterEpoch)
	if n := allowedAt(&rl, past, capacity+1); n != capacity {
		t.Fatalf("a backwards clock left %d messages of burst, want %d", n, capacity)
	}

	// Nor may it move the baseline: if it had, this call would see an hour of
	// elapsed time rather than nothing since the epoch.
	if rl.allowAt(rateLimiterEpoch) {
		t.Fatal("limiter refilled from a rewound baseline")
	}
}

// BenchmarkRateLimiterAllow measures the production entry point, clock read
// included: the read pump calls allow once per message, so timing allowAt
// instead would leave out work the hot path really does.
func BenchmarkRateLimiterAllow(b *testing.B) {
	rl := newRateLimiter(1_000_000, time.Second)

	b.ReportAllocs()
	for b.Loop() {
		rl.allow()
	}
}
