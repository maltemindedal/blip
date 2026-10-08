// Package server provides configuration helpers that define runtime defaults,
// validation, and rate-limiting parameters for the Blip service.
package server

import (
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Default configuration values applied when a setting is unset or invalid.
const (
	defaultPort            = ":8080"
	defaultMaxMessageSize  = 512
	defaultRateLimitBurst  = 5
	defaultRateLimitRefill = time.Second
	defaultAllowedOrigin   = "http://localhost:8080"
)

// maxRefillSeconds is the largest RATE_LIMIT_REFILL_INTERVAL that still fits a
// [time.Duration] once multiplied by [time.Second].
const maxRefillSeconds = math.MaxInt64 / int64(time.Second)

// RateLimitConfig defines the parameters for per-connection message rate limiting.
type RateLimitConfig struct {
	Burst          int
	RefillInterval time.Duration
}

// Config holds the server configuration settings including security controls.
type Config struct {
	Port           string
	AllowedOrigins []string
	MaxMessageSize int64
	RateLimit      RateLimitConfig
}

// resolvedConfig is an immutable, fully resolved view of the configuration:
// defaults substituted and the origin allow-list turned into an [originPolicy].
// A [Hub] owns one, built once at construction, so the hot paths (origin checks,
// client construction) read it without locking and two hubs in one process can
// be configured differently.
type resolvedConfig struct {
	Config
	origins originPolicy
}

func defaultConfig() Config {
	return Config{
		Port:           defaultPort,
		AllowedOrigins: []string{defaultAllowedOrigin},
		MaxMessageSize: defaultMaxMessageSize,
		RateLimit: RateLimitConfig{
			Burst:          defaultRateLimitBurst,
			RefillInterval: defaultRateLimitRefill,
		},
	}
}

// resolveConfig substitutes defaults for invalid values and resolves the origin
// allow-list into an [originPolicy]. A nil cfg resolves to the defaults.
//
// The caller's Config is copied, so mutating it afterwards cannot change a hub
// that has already been built from it. The result is never mutated again.
func resolveConfig(cfg *Config) resolvedConfig {
	resolved := defaultConfig()
	if cfg != nil {
		// Whole-struct assignment, not a field-by-field literal: a field added to
		// Config is carried across by default rather than silently dropped by a
		// literal that forgot to list it.
		//
		// It is a shallow copy, so it only makes that promise for value-typed
		// fields. A reference-typed field added to Config would arrive aliased to
		// the caller's, which breaks the guarantee above and which neither the
		// compiler nor TestResolveConfigPreservesEveryField can see. Give any such
		// field its own copy here, as AllowedOrigins gets below.
		resolved = *cfg
		resolved.AllowedOrigins = slices.Clone(cfg.AllowedOrigins)
	}

	switch {
	case resolved.Port == "":
		resolved.Port = defaultPort
	case !strings.Contains(resolved.Port, ":"):
		resolved.Port = ":" + resolved.Port
	}

	if resolved.MaxMessageSize <= 0 {
		resolved.MaxMessageSize = defaultMaxMessageSize
	}

	if resolved.RateLimit.Burst <= 0 {
		resolved.RateLimit.Burst = defaultRateLimitBurst
	}

	if resolved.RateLimit.RefillInterval <= 0 {
		resolved.RateLimit.RefillInterval = defaultRateLimitRefill
	}

	return resolvedConfig{Config: resolved, origins: newOriginPolicy(resolved.AllowedOrigins)}
}

// NewConfig creates a Config instance populated with default values for all settings.
func NewConfig() *Config {
	cfg := defaultConfig()
	return &cfg
}

// NewConfigFromEnv creates a Config instance from environment variables.
// Falls back to default values if environment variables are not set.
func NewConfigFromEnv() *Config {
	cfg := defaultConfig()

	if port := os.Getenv("SERVER_PORT"); port != "" {
		cfg.Port = port
	}

	if origins := os.Getenv("ALLOWED_ORIGINS"); origins != "" {
		cfg.AllowedOrigins = parseOrigins(origins)
	}

	cfg.MaxMessageSize = positiveIntFromEnv("MAX_MESSAGE_SIZE", cfg.MaxMessageSize, math.MaxInt64)
	cfg.RateLimit.Burst = positiveIntFromEnv("RATE_LIMIT_BURST", cfg.RateLimit.Burst, math.MaxInt64)

	refillSeconds := positiveIntFromEnv("RATE_LIMIT_REFILL_INTERVAL",
		int64(cfg.RateLimit.RefillInterval/time.Second), maxRefillSeconds)
	cfg.RateLimit.RefillInterval = time.Duration(refillSeconds) * time.Second

	return &cfg
}

func parseOrigins(origins string) []string {
	parts := strings.Split(origins, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// positiveIntFromEnv reads name and returns it as an integer greater than zero
// and no greater than limit. An unset variable, a value that does not parse, a
// value that is zero or negative, and a value above limit or too large for T all
// yield fallback; all but the first are logged first.
func positiveIntFromEnv[T int | int64](name string, fallback T, limit int64) T {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}

	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 || parsed > limit || int64(T(parsed)) != parsed {
		log().Warn("invalid "+name+"; using default", "value", raw, "default", fallback)
		return fallback
	}

	return T(parsed)
}
