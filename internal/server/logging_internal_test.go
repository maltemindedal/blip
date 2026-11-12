package server

import (
	"log/slog"
	"testing"
)

// TestLogLevelFromEnv pins how LOG_LEVEL is read: a recognized level is taken,
// case-insensitively and trimmed, and an unset or unparsable value falls back to
// info. The logger it feeds is not exercised.
//
// t.Setenv rules out t.Parallel.
func TestLogLevelFromEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  slog.Level
	}{
		{name: "unset", want: slog.LevelInfo},
		{name: "debug", value: "debug", want: slog.LevelDebug},
		{name: "upper case", value: "WARN", want: slog.LevelWarn},
		{name: "padded", value: "  error ", want: slog.LevelError},
		{name: "not a level", value: "verbose", want: slog.LevelInfo},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tc.value)

			if got := LogLevelFromEnv(); got != tc.want {
				t.Errorf("LogLevelFromEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}
