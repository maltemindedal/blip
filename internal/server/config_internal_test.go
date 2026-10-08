package server

import (
	"bytes"
	"log/slog"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestResolveConfigPreservesEveryField pins that resolution is lossless for a
// Config that needs nothing substituted.
//
// Two halves, because the comparison alone is not enough. assertEveryFieldIsSet
// fails if the literal below leaves any field of [Config] at its zero value, so
// a field added to the struct stops this test until someone gives it a value
// here. Only then does the [reflect.DeepEqual] comparison mean anything: with
// every field set to something non-zero, a field [resolveConfig] drops comes
// back zero and the comparison catches it. Without the first half the second is
// vacuous — an unset field is zero on both sides and compares equal, which is
// the same enumerate-every-field trap this test exists to guard against, merely
// moved from resolveConfig into the test.
//
// Neither half catches aliasing. DeepEqual compares what a reference field
// points at, not whether both sides point at the same thing, so a field that
// arrives sharing the caller's backing array rather than copied passes here.
// That hazard is guarded by the comment in resolveConfig.
func TestResolveConfigPreservesEveryField(t *testing.T) {
	t.Parallel()

	// Every value is non-default and already normalized, so nothing in the
	// result can differ because it was resolved rather than dropped.
	cfg := Config{
		Port:           ":9443",
		AllowedOrigins: []string{"https://example.com"},
		MaxMessageSize: 4096,
		RateLimit: RateLimitConfig{
			Burst:          42,
			RefillInterval: 3 * time.Second,
		},
	}

	assertEveryFieldIsSet(t, reflect.ValueOf(cfg), "Config")

	resolved := resolveConfig(&cfg)

	if !reflect.DeepEqual(resolved.Config, cfg) {
		t.Errorf("resolveConfig(%+v) resolved to %+v, want the input carried across unchanged",
			cfg, resolved.Config)
	}
}

// assertEveryFieldIsSet fails for each field of v left at its zero value. It
// recurses into nested structs declared in this package — RateLimitConfig
// today — so a field added one level down is caught too, and stops at types
// from elsewhere rather than picking over their internals.
func assertEveryFieldIsSet(t *testing.T, v reflect.Value, path string) {
	t.Helper()

	structType := v.Type()

	for i := range structType.NumField() {
		field := structType.Field(i)
		value := v.Field(i)
		name := path + "." + field.Name

		if value.Kind() == reflect.Struct && field.Type.PkgPath() == structType.PkgPath() {
			assertEveryFieldIsSet(t, value, name)
			continue
		}

		if value.IsZero() {
			t.Errorf("%s is left at its zero value by this test; give it a non-zero value so "+
				"the comparison can tell a field resolveConfig dropped from one never set", name)
		}
	}
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
