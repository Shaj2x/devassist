// Package config reads service settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Base holds settings every DevAssist Go service needs.
type Base struct {
	Env                 string
	LogLevel            string
	Port                int
	DatabaseURL         string
	RedisURL            string
	KafkaBrokers        []string
	KafkaGroupPrefix    string
	ReadinessTimeout    time.Duration
	ShutdownGracePeriod time.Duration
}

// LoadBase reads the shared settings. defaultPort is used when PORT is unset.
func LoadBase(defaultPort int) (Base, error) {
	port, err := Int("PORT", defaultPort)
	if err != nil {
		return Base{}, err
	}
	readiness, err := Duration("READINESS_TIMEOUT", 2*time.Second)
	if err != nil {
		return Base{}, err
	}
	grace, err := Duration("SHUTDOWN_GRACE_PERIOD", 10*time.Second)
	if err != nil {
		return Base{}, err
	}
	return Base{
		Env:                 String("DEVASSIST_ENV", "development"),
		LogLevel:            String("LOG_LEVEL", "INFO"),
		Port:                port,
		DatabaseURL:         String("DATABASE_URL", "postgresql://devassist:devassist@localhost:5432/devassist"),
		RedisURL:            String("REDIS_URL", "redis://localhost:6379/0"),
		KafkaBrokers:        List("KAFKA_BOOTSTRAP_SERVERS", []string{"localhost:29092"}),
		KafkaGroupPrefix:    String("KAFKA_CONSUMER_GROUP_PREFIX", "devassist"),
		ReadinessTimeout:    readiness,
		ShutdownGracePeriod: grace,
	}, nil
}

// String returns the variable's value, or def when it is unset or empty.
func String(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// Int parses an integer variable.
func Int(key string, def int) (int, error) {
	v := String(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	return n, nil
}

// Duration parses a Go duration ("30s", "5m") or a bare number of seconds.
func Duration(key string, def time.Duration) (time.Duration, error) {
	v := String(key, "")
	if v == "" {
		return def, nil
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(secs * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration", key, v)
	}
	return d, nil
}

// List splits a comma-separated variable, trimming blanks.
func List(key string, def []string) []string {
	v := String(key, "")
	if v == "" {
		return def
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
