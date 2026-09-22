// Package config loads 12-factor configuration from environment variables
// with validation and defaults that make `make run` work with nothing else.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved service configuration.
type Config struct {
	ServiceName string
	Environment string
	LogLevel    string
	LogFormat   string // json | text

	HTTPAddr        string
	ShutdownTimeout time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	MaxBodyBytes    int64

	StoreBackend  string // memory | postgres
	PostgresDSN   string
	MigrationsDir string

	CMMSBackend string // simulated
	// WorkOrderRetries bounds CMMS retries per alert episode.
	WorkOrderRetries int

	KafkaEnabled bool
	KafkaBrokers []string
	KafkaTopic   string
	KafkaGroup   string

	OTLPEndpoint string // empty disables trace export
	OTLPInsecure bool
	TraceRatio   float64
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	c := Config{
		ServiceName:   env("SERVICE_NAME", "predictmaint"),
		Environment:   env("ENVIRONMENT", "dev"),
		LogLevel:      strings.ToLower(env("LOG_LEVEL", "info")),
		LogFormat:     strings.ToLower(env("LOG_FORMAT", "json")),
		HTTPAddr:      env("HTTP_ADDR", ":8080"),
		StoreBackend:  strings.ToLower(env("STORE_BACKEND", "memory")),
		PostgresDSN:   env("POSTGRES_DSN", ""),
		MigrationsDir: env("MIGRATIONS_DIR", "migrations"),
		CMMSBackend:   strings.ToLower(env("CMMS_BACKEND", "simulated")),
		KafkaTopic:    env("KAFKA_TOPIC", "uns.metrics"),
		KafkaGroup:    env("KAFKA_GROUP", "predictmaint"),
		OTLPEndpoint:  env("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
	}
	var err error
	var errs []error
	if c.ShutdownTimeout, err = envDuration("SHUTDOWN_TIMEOUT", 20*time.Second); err != nil {
		errs = append(errs, err)
	}
	if c.ReadTimeout, err = envDuration("HTTP_READ_TIMEOUT", 15*time.Second); err != nil {
		errs = append(errs, err)
	}
	if c.WriteTimeout, err = envDuration("HTTP_WRITE_TIMEOUT", 30*time.Second); err != nil {
		errs = append(errs, err)
	}
	if c.MaxBodyBytes, err = envInt64("HTTP_MAX_BODY_BYTES", 16<<20); err != nil {
		errs = append(errs, err)
	}
	if c.WorkOrderRetries, err = envInt("WORKORDER_RETRIES", 5); err != nil {
		errs = append(errs, err)
	}
	if c.KafkaEnabled, err = envBool("KAFKA_ENABLED", false); err != nil {
		errs = append(errs, err)
	}
	if c.OTLPInsecure, err = envBool("OTEL_EXPORTER_OTLP_INSECURE", true); err != nil {
		errs = append(errs, err)
	}
	if c.TraceRatio, err = envFloat("OTEL_TRACES_SAMPLER_ARG", 1.0); err != nil {
		errs = append(errs, err)
	}
	if b := env("KAFKA_BROKERS", "localhost:9092"); b != "" {
		for _, s := range strings.Split(b, ",") {
			if s = strings.TrimSpace(s); s != "" {
				c.KafkaBrokers = append(c.KafkaBrokers, s)
			}
		}
	}
	if len(errs) > 0 {
		return c, errors.Join(errs...)
	}
	return c, c.Validate()
}

// Validate checks cross-field constraints.
func (c Config) Validate() error {
	var errs []error
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug|info|warn|error, got %q", c.LogLevel))
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json|text, got %q", c.LogFormat))
	}
	if c.HTTPAddr == "" {
		errs = append(errs, errors.New("HTTP_ADDR is required"))
	}
	switch c.StoreBackend {
	case "memory":
	case "postgres":
		if c.PostgresDSN == "" {
			errs = append(errs, errors.New("POSTGRES_DSN is required when STORE_BACKEND=postgres"))
		}
	default:
		errs = append(errs, fmt.Errorf("STORE_BACKEND must be memory|postgres, got %q", c.StoreBackend))
	}
	if c.CMMSBackend != "simulated" {
		errs = append(errs, fmt.Errorf("CMMS_BACKEND must be simulated, got %q", c.CMMSBackend))
	}
	if c.KafkaEnabled && len(c.KafkaBrokers) == 0 {
		errs = append(errs, errors.New("KAFKA_BROKERS is required when KAFKA_ENABLED=true"))
	}
	if c.ShutdownTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 {
		errs = append(errs, errors.New("timeouts must be positive"))
	}
	if c.MaxBodyBytes < 1024 {
		errs = append(errs, errors.New("HTTP_MAX_BODY_BYTES must be >= 1024"))
	}
	if c.WorkOrderRetries < 0 {
		errs = append(errs, errors.New("WORKORDER_RETRIES must be >= 0"))
	}
	if c.TraceRatio < 0 || c.TraceRatio > 1 {
		errs = append(errs, errors.New("OTEL_TRACES_SAMPLER_ARG must be in [0,1]"))
	}
	return errors.Join(errs...)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func envInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return i, nil
}

func envInt64(key string, def int64) (int64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return i, nil
}

func envBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

func envFloat(key string, def float64) (float64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}
