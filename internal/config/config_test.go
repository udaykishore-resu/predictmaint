package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":8080", c.HTTPAddr)
	assert.Equal(t, "memory", c.StoreBackend)
	assert.Equal(t, "simulated", c.CMMSBackend)
	assert.Equal(t, 20*time.Second, c.ShutdownTimeout)
	assert.Equal(t, []string{"localhost:9092"}, c.KafkaBrokers)
	assert.False(t, c.KafkaEnabled)
	assert.Equal(t, 1.0, c.TraceRatio)
}

func TestLoadOverridesAndErrors(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":18801")
	t.Setenv("STORE_BACKEND", "postgres")
	t.Setenv("POSTGRES_DSN", "postgres://x")
	t.Setenv("KAFKA_ENABLED", "true")
	t.Setenv("KAFKA_BROKERS", "a:1, b:2 ,")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("HTTP_MAX_BODY_BYTES", "4096")
	t.Setenv("WORKORDER_RETRIES", "2")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.5")
	t.Setenv("LOG_FORMAT", "text")
	c, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":18801", c.HTTPAddr)
	assert.Equal(t, []string{"a:1", "b:2"}, c.KafkaBrokers)
	assert.Equal(t, 5*time.Second, c.ShutdownTimeout)
	assert.Equal(t, int64(4096), c.MaxBodyBytes)
	assert.Equal(t, 2, c.WorkOrderRetries)
	assert.Equal(t, 0.5, c.TraceRatio)

	t.Setenv("SHUTDOWN_TIMEOUT", "soon")
	t.Setenv("WORKORDER_RETRIES", "x")
	t.Setenv("HTTP_MAX_BODY_BYTES", "big")
	t.Setenv("KAFKA_ENABLED", "maybe")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "half")
	_, err = Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SHUTDOWN_TIMEOUT")
	assert.Contains(t, err.Error(), "WORKORDER_RETRIES")
	assert.Contains(t, err.Error(), "HTTP_MAX_BODY_BYTES")
	assert.Contains(t, err.Error(), "KAFKA_ENABLED")
	assert.Contains(t, err.Error(), "OTEL_TRACES_SAMPLER_ARG")
}

func TestValidate(t *testing.T) {
	base := func() Config {
		return Config{LogLevel: "info", LogFormat: "json", HTTPAddr: ":1", StoreBackend: "memory", CMMSBackend: "simulated",
			ShutdownTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, MaxBodyBytes: 4096, TraceRatio: 1}
	}
	require.NoError(t, base().Validate())
	cases := map[string]func(*Config){
		"log level":     func(c *Config) { c.LogLevel = "loud" },
		"log format":    func(c *Config) { c.LogFormat = "xml" },
		"addr":          func(c *Config) { c.HTTPAddr = "" },
		"store":         func(c *Config) { c.StoreBackend = "redis" },
		"pg dsn":        func(c *Config) { c.StoreBackend = "postgres" },
		"cmms":          func(c *Config) { c.CMMSBackend = "maximo" },
		"kafka brokers": func(c *Config) { c.KafkaEnabled = true },
		"timeout":       func(c *Config) { c.ReadTimeout = 0 },
		"body":          func(c *Config) { c.MaxBodyBytes = 10 },
		"retries":       func(c *Config) { c.WorkOrderRetries = -1 },
		"trace ratio":   func(c *Config) { c.TraceRatio = 2 },
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			c := base()
			m(&c)
			assert.Error(t, c.Validate())
		})
	}
}
