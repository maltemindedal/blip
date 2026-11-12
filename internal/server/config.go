// Package server provides configuration helpers that define runtime defaults,
// validation, and rate-limiting parameters for the Blip service.
package server

import (
	"math"
	"os"
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

type settings struct {
	addr           string
	origins        originPolicy
	maxMessageSize int64
	rateLimit      rateLimit
}

type rateLimit struct {
	burst  int
	refill time.Duration
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

func resolveConfig(cfg *Config) settings {
	raw := defaultConfig()
	if cfg != nil {
		raw = *cfg
	}

	s := settings{
		addr:           raw.Port,
		origins:        newOriginPolicy(raw.AllowedOrigins),
		maxMessageSize: raw.MaxMessageSize,
		rateLimit:      rateLimit{burst: raw.RateLimit.Burst, refill: raw.RateLimit.RefillInterval},
	}

	switch {
	case s.addr == "":
		s.addr = defaultPort
	case !strings.Contains(s.addr, ":"):
		s.addr = ":" + s.addr
	}

	if s.maxMessageSize <= 0 {
		s.maxMessageSize = defaultMaxMessageSize
	}

	if s.rateLimit.burst <= 0 {
		s.rateLimit.burst = defaultRateLimitBurst
	}

	if s.rateLimit.refill <= 0 {
		s.rateLimit.refill = defaultRateLimitRefill
	}

	return s
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
		log().Warn("invalid "+name+"; using default", "value", raw, "default", fallback) //nolint:sloglint // per-variable msg is the operator string shown in docs/reference/configuration.md
		return fallback
	}

	return T(parsed)
}
