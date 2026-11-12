package server

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestResolveConfigCarriesValidValuesAcross pins that resolution changes
// nothing in a Config that needs nothing substituted.
//
// It does not cover a field added to Config later, which
// TestEveryConfigFieldReachesTheSettings does, or the substituted defaults.
func TestResolveConfigCarriesValidValuesAcross(t *testing.T) {
	t.Parallel()

	got := resolveConfig(&Config{
		Port:           ":9443",
		AllowedOrigins: []string{"https://example.com"},
		MaxMessageSize: 4096,
		RateLimit:      RateLimitConfig{Burst: 42, RefillInterval: 3 * time.Second},
	})

	want := settings{
		addr:           ":9443",
		origins:        originPolicy{origins: map[string]struct{}{"https://example.com": {}}},
		maxMessageSize: 4096,
		rateLimit:      rateLimit{burst: 42, refill: 3 * time.Second},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveConfig resolved to %+v, want %+v", got, want)
	}
}

// TestEveryConfigFieldReachesTheSettings pins that no field of [Config] is
// dead: changing any one of them, however deeply nested, changes the settings
// [resolveConfig] returns. It walks the struct by reflection instead of naming
// fields, so a field added to Config and not read by resolveConfig fails here
// without anyone remembering to list it. Because settings is the only thing
// production code past [New] can read, a field that reaches the settings is
// a field that can take effect, and one that does not cannot.
//
// It does not check that the value arrives unchanged, which
// TestResolveConfigCarriesValidValuesAcross does for today's fields, nor that
// anything reads the settings field it lands in.
func TestEveryConfigFieldReachesTheSettings(t *testing.T) {
	t.Parallel()

	baseline := resolveConfig(NewConfig())

	for _, path := range leafFields(reflect.TypeFor[Config](), nil) {
		cfg := NewConfig()
		field := reflect.ValueOf(cfg).Elem().FieldByIndex(path.index)

		switch field.Kind() {
		case reflect.String:
			field.SetString("9443")
		case reflect.Int, reflect.Int64:
			field.SetInt(field.Int() + 7)
		case reflect.Slice:
			field.Set(reflect.ValueOf([]string{"https://changed.example"}))
		default:
			t.Fatalf("Config.%s has kind %s, which this test cannot change; teach it how", path.name, field.Kind())
		}

		if reflect.DeepEqual(resolveConfig(cfg), baseline) {
			t.Errorf("changing Config.%s changed nothing in the resolved settings: "+
				"resolveConfig does not read it, so it can have no effect", path.name)
		}
	}
}

type fieldPath struct {
	name  string
	index []int
}

func leafFields(t reflect.Type, prefix []int) []fieldPath {
	var out []fieldPath

	for i := range t.NumField() {
		field := t.Field(i)
		index := append(slices.Clone(prefix), i)

		if field.Type.Kind() == reflect.Struct {
			for _, leaf := range leafFields(field.Type, index) {
				out = append(out, fieldPath{field.Name + "." + leaf.name, leaf.index})
			}
			continue
		}

		out = append(out, fieldPath{field.Name, index})
	}

	return out
}

// TestRawConfigStopsAtTheBoundary pins the rule that keeps raw and resolved
// values apart: outside config.go, production code names [Config] in one place,
// the parameter list of [New], and New does one thing with that parameter,
// which is to hand it to [resolveConfig]. Anything else that held a Config (a
// struct field, a second parameter, a read of cfg.Port in New) could use a raw
// value where a resolved one is meant.
//
// It parses this package's sources and matches identifiers by name. It does not
// catch a raw value that config.go itself hands out as a plain string or int.
func TestRawConfigStopsAtTheBoundary(t *testing.T) {
	t.Parallel()

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Failed to list package sources: %v", err)
	}

	fset := token.NewFileSet()
	foundNew := false

	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") || path == "config.go" {
			continue
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("Failed to parse %s: %v", path, err)
		}

		inNewParams := map[*ast.Ident]bool{}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "New" {
				continue
			}
			foundNew = true

			ast.Inspect(fn.Type.Params, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					inNewParams[id] = true
				}
				return true
			})
			checkNewOnlyResolves(t, fset, fn)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || inNewParams[id] || (id.Name != "Config" && id.Name != "RateLimitConfig") {
				return true
			}

			t.Errorf("%s names %s. Past New, production code reads the resolved settings: "+
				"take a settings (or one field of it) instead, and add what you need to settings in config.go",
				fset.Position(id.Pos()), id.Name)
			return true
		})
	}

	if !foundNew {
		t.Fatal("found no func New outside config.go; the configuration boundary is unguarded")
	}
}

func checkNewOnlyResolves(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) {
	t.Helper()

	if len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) != 1 {
		t.Errorf("%s: New takes something other than one named Config parameter", fset.Position(fn.Pos()))
		return
	}
	param := fn.Type.Params.List[0].Names[0].Name

	resolved := map[*ast.Ident]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		callee, isIdent := call.Fun.(*ast.Ident)
		arg, isArg := call.Args[0].(*ast.Ident)
		if isIdent && isArg && callee.Name == "resolveConfig" {
			resolved[arg] = true
		}
		return true
	})

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == param && !resolved[id] {
			t.Errorf("%s: New uses its raw Config for something other than resolveConfig(%s). "+
				"Read the value from the settings resolveConfig returns instead.",
				fset.Position(id.Pos()), param)
		}
		return true
	})
}

// TestNewConfigFromEnvRateLimit pins how the rate limit variables are read: in
// range values are taken, and anything that is not a positive integer the
// platform can represent falls back to the default. The overflow rows are the
// regression cases: a refill interval whose conversion to a [time.Duration]
// overflows used to wrap to an arbitrary duration (18446744074 seconds became
// 290ms, a far weaker limiter) instead of falling back, and a burst too large
// for int used to truncate on a 32-bit platform.
//
// t.Setenv rules out t.Parallel.
func TestNewConfigFromEnvRateLimit(t *testing.T) {
	const maxRefillSeconds = 9223372036 // math.MaxInt64 / int64(time.Second)

	// A burst that does not fit int is only rejected where int is 32 bits wide, so
	// that row pins the narrowing guard on GOARCH=386 alone; CI does not run 386.
	wantHugeBurst := int64(math.MaxInt64)
	if strconv.IntSize == 32 {
		wantHugeBurst = defaultRateLimitBurst
	}

	tests := []struct {
		name       string
		burst      string
		refill     string
		wantBurst  int64
		wantRefill time.Duration
	}{
		{name: "unset", wantBurst: defaultRateLimitBurst, wantRefill: defaultRateLimitRefill},
		{name: "in range", burst: "10", refill: "2", wantBurst: 10, wantRefill: 2 * time.Second},
		{name: "zero", burst: "0", refill: "0", wantBurst: defaultRateLimitBurst, wantRefill: defaultRateLimitRefill},
		{name: "negative", burst: "-1", refill: "-1", wantBurst: defaultRateLimitBurst, wantRefill: defaultRateLimitRefill},
		{name: "not a number", burst: "abc", refill: "1s", wantBurst: defaultRateLimitBurst, wantRefill: defaultRateLimitRefill},
		{name: "refill at the Duration limit", refill: "9223372036",
			wantBurst: defaultRateLimitBurst, wantRefill: maxRefillSeconds * time.Second},
		{name: "refill one past the limit", refill: "9223372037",
			wantBurst: defaultRateLimitBurst, wantRefill: defaultRateLimitRefill},
		{name: "refill wraps to a short interval", refill: "18446744074",
			wantBurst: defaultRateLimitBurst, wantRefill: defaultRateLimitRefill},
		{name: "refill beyond int64", refill: "99999999999999999999",
			wantBurst: defaultRateLimitBurst, wantRefill: defaultRateLimitRefill},
		{name: "burst beyond int32", burst: "9223372036854775807",
			wantBurst: wantHugeBurst, wantRefill: defaultRateLimitRefill},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RATE_LIMIT_BURST", tc.burst)
			t.Setenv("RATE_LIMIT_REFILL_INTERVAL", tc.refill)

			cfg := NewConfigFromEnv()

			if got := int64(cfg.RateLimit.Burst); got != tc.wantBurst {
				t.Errorf("Burst = %d, want %d", got, tc.wantBurst)
			}
			if cfg.RateLimit.RefillInterval != tc.wantRefill {
				t.Errorf("RefillInterval = %v, want %v", cfg.RateLimit.RefillInterval, tc.wantRefill)
			}
		})
	}
}

// TestNewConfigFromEnvMaxMessageSize pins how MAX_MESSAGE_SIZE is read. It shares
// positiveIntFromEnv with the rate limit variables but is an int64 end to end.
//
// t.Setenv rules out t.Parallel.
func TestNewConfigFromEnvMaxMessageSize(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int64
	}{
		{name: "unset", want: defaultMaxMessageSize},
		{name: "in range", value: "1024", want: 1024},
		{name: "zero", value: "0", want: defaultMaxMessageSize},
		{name: "negative", value: "-1", want: defaultMaxMessageSize},
		{name: "not a number", value: "abc", want: defaultMaxMessageSize},
		{name: "largest int64", value: "9223372036854775807", want: math.MaxInt64},
		{name: "beyond int64", value: "9223372036854775808", want: defaultMaxMessageSize},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MAX_MESSAGE_SIZE", tc.value)

			if got := NewConfigFromEnv().MaxMessageSize; got != tc.want {
				t.Errorf("MaxMessageSize = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestNewConfigFromEnvPort pins that SERVER_PORT is taken verbatim and that an
// unset or empty one leaves the default. It does not check that the value is a
// valid listen address; Service.Run reports that when it binds.
//
// t.Setenv rules out t.Parallel.
func TestNewConfigFromEnvPort(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "unset", want: defaultPort},
		{name: "port only", value: ":9000", want: ":9000"},
		{name: "host and port", value: "127.0.0.1:9000", want: "127.0.0.1:9000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SERVER_PORT", tc.value)

			if got := NewConfigFromEnv().Port; got != tc.want {
				t.Errorf("Port = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNewConfigFromEnvAllowedOrigins pins that ALLOWED_ORIGINS is split on
// commas with each entry trimmed, and that an unset one keeps the default list.
// It checks the raw list on Config; which entries the origin policy then accepts
// is TestOriginPolicyAllows's job.
//
// t.Setenv rules out t.Parallel.
func TestNewConfigFromEnvAllowedOrigins(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "unset", want: []string{defaultAllowedOrigin}},
		{name: "single", value: "https://example.com", want: []string{"https://example.com"}},
		{name: "several, trimmed", value: "https://a.example, https://b.example ,*",
			want: []string{"https://a.example", "https://b.example", "*"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ALLOWED_ORIGINS", tc.value)

			if got := NewConfigFromEnv().AllowedOrigins; !slices.Equal(got, tc.want) {
				t.Errorf("AllowedOrigins = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNewConfigFromEnvWarnsOnInvalidValues pins that a value falling back to the
// default says so, naming the variable and echoing the raw value, and that a
// usable or unset value stays silent. Without it, dropping the warning would
// leave every value assertion above green.
//
// It swaps the process-wide logger and uses t.Setenv, so it cannot be parallel.
func TestNewConfigFromEnvWarnsOnInvalidValues(t *testing.T) {
	var logs bytes.Buffer
	SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { SetLogger(slog.New(slog.DiscardHandler)) })

	tests := []struct {
		env   string
		value string
		warns bool
	}{
		{"MAX_MESSAGE_SIZE", "", false},
		{"MAX_MESSAGE_SIZE", "2048", false},
		{"MAX_MESSAGE_SIZE", "abc", true},
		{"MAX_MESSAGE_SIZE", "0", true},
		{"MAX_MESSAGE_SIZE", "-1", true},
		{"RATE_LIMIT_BURST", "10", false},
		{"RATE_LIMIT_BURST", "-1", true},
		{"RATE_LIMIT_REFILL_INTERVAL", "9223372036", false},
		{"RATE_LIMIT_REFILL_INTERVAL", "1s", true},
		{"RATE_LIMIT_REFILL_INTERVAL", "9223372037", true},
		{"RATE_LIMIT_REFILL_INTERVAL", "18446744074", true},
	}

	for _, tc := range tests {
		t.Run(tc.env+"="+tc.value, func(t *testing.T) {
			logs.Reset()
			t.Setenv("MAX_MESSAGE_SIZE", "")
			t.Setenv("RATE_LIMIT_BURST", "")
			t.Setenv("RATE_LIMIT_REFILL_INTERVAL", "")
			t.Setenv(tc.env, tc.value)

			NewConfigFromEnv()

			out := logs.String()
			if !tc.warns {
				if out != "" {
					t.Errorf("Expected no log output, got %q", out)
				}
				return
			}
			for _, want := range []string{"level=WARN", "invalid " + tc.env + "; using default", "value=" + tc.value, "default="} {
				if !strings.Contains(out, want) {
					t.Errorf("Expected %q in the log, got %q", want, out)
				}
			}
		})
	}
}

// TestNewConfig tests the configuration creation function.
// It verifies that NewConfig returns a properly initialized Config
// struct with the expected default values.
func TestNewConfig(t *testing.T) {
	t.Parallel()

	config := NewConfig()

	if config == nil {
		t.Fatal("NewConfig returned nil")
	}

	expectedPort := ":8080"
	if config.Port != expectedPort {
		t.Errorf("Expected default port %s, got %s", expectedPort, config.Port)
	}
}
