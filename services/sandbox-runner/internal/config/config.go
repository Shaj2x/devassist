// Package config holds the sandbox runner's settings.
package config

import (
	"fmt"
	"strconv"
	"time"

	base "github.com/Shaj2x/devassist/libs/gocommon/config"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/sandbox"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/toolchain"
	"github.com/docker/go-units"
)

type Config struct {
	base.Base
	Images         toolchain.Images
	Limits         sandbox.Limits
	EgressNetwork  string // "" = never give sandboxes network access
	Timeout        time.Duration
	StepTimeout    time.Duration
	Concurrency    int
	WorkDir        string
	GitHubToken    string
	ResultCacheTTL time.Duration
}

func Load() (Config, error) {
	b, err := base.LoadBase(8081)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Base: b,
		Images: toolchain.Images{
			"python": base.String("SANDBOX_IMAGE_PYTHON", toolchain.DefaultImages["python"]),
			"go":     base.String("SANDBOX_IMAGE_GO", toolchain.DefaultImages["go"]),
			"node":   base.String("SANDBOX_IMAGE_NODE", toolchain.DefaultImages["node"]),
		},
		EgressNetwork: base.String("SANDBOX_EGRESS_NETWORK", "devassist-sandbox-egress"),
		WorkDir:       base.String("SANDBOX_WORK_DIR", "/tmp/devassist-sandbox"),
		GitHubToken:   base.String("GITHUB_TOKEN", ""),
	}
	if base.String("SANDBOX_INSTALL_NETWORK", "true") == "false" {
		cfg.EgressNetwork = ""
	}

	mem, err := units.RAMInBytes(base.String("SANDBOX_MEMORY", "1g"))
	if err != nil {
		return Config{}, fmt.Errorf("SANDBOX_MEMORY: %w", err)
	}
	tmpfs, err := units.RAMInBytes(base.String("SANDBOX_TMPFS_SIZE", "1g"))
	if err != nil {
		return Config{}, fmt.Errorf("SANDBOX_TMPFS_SIZE: %w", err)
	}
	cpus, err := strconv.ParseFloat(base.String("SANDBOX_CPUS", "1"), 64)
	if err != nil || cpus <= 0 {
		return Config{}, fmt.Errorf("SANDBOX_CPUS must be a positive number")
	}
	pids, err := base.Int("SANDBOX_PIDS", 256)
	if err != nil {
		return Config{}, err
	}
	cfg.Limits = sandbox.Limits{MemoryBytes: mem, NanoCPUs: int64(cpus * 1e9), PIDs: int64(pids), TmpfsBytes: tmpfs}

	if cfg.Concurrency, err = base.Int("SANDBOX_CONCURRENCY", 2); err != nil {
		return Config{}, err
	}
	for _, d := range []struct {
		dst *time.Duration
		key string
		def time.Duration
	}{
		{&cfg.Timeout, "SANDBOX_TIMEOUT", 5 * time.Minute},
		{&cfg.StepTimeout, "SANDBOX_STEP_TIMEOUT", 3 * time.Minute},
		{&cfg.ResultCacheTTL, "SANDBOX_RESULT_TTL", 24 * time.Hour},
	} {
		if *d.dst, err = base.Duration(d.key, d.def); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}
