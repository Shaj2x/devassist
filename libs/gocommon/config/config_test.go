package config

import (
	"testing"
	"time"
)

func TestLoadBaseDefaults(t *testing.T) {
	for _, k := range []string{"PORT", "DATABASE_URL", "KAFKA_BOOTSTRAP_SERVERS", "READINESS_TIMEOUT"} {
		t.Setenv(k, "")
	}
	cfg, err := LoadBase(8080)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.ReadinessTimeout != 2*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if len(cfg.KafkaBrokers) != 1 || cfg.KafkaBrokers[0] != "localhost:29092" {
		t.Fatalf("unexpected brokers: %v", cfg.KafkaBrokers)
	}
}

func TestLoadBaseFromEnv(t *testing.T) {
	t.Setenv("PORT", "9000")
	t.Setenv("KAFKA_BOOTSTRAP_SERVERS", "a:9092, b:9092 ,")
	t.Setenv("READINESS_TIMEOUT", "1.5")
	cfg, err := LoadBase(8080)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9000 {
		t.Errorf("port = %d", cfg.Port)
	}
	if got := cfg.KafkaBrokers; len(got) != 2 || got[0] != "a:9092" || got[1] != "b:9092" {
		t.Errorf("brokers = %v", got)
	}
	if cfg.ReadinessTimeout != 1500*time.Millisecond {
		t.Errorf("readiness = %v", cfg.ReadinessTimeout)
	}
}

func TestInvalidValues(t *testing.T) {
	t.Setenv("PORT", "eighty")
	if _, err := LoadBase(8080); err == nil {
		t.Fatal("expected error for non-integer PORT")
	}
	t.Setenv("PORT", "")
	t.Setenv("READINESS_TIMEOUT", "soon")
	if _, err := LoadBase(8080); err == nil {
		t.Fatal("expected error for bad duration")
	}
}

func TestDurationAcceptsGoSyntax(t *testing.T) {
	t.Setenv("X_TIMEOUT", "250ms")
	d, err := Duration("X_TIMEOUT", time.Second)
	if err != nil || d != 250*time.Millisecond {
		t.Fatalf("got %v, %v", d, err)
	}
}
