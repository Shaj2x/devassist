// Package sandbox runs untrusted code in locked-down, disposable containers.
//
// Isolation, layer by layer (see HostConfig):
//
//   - no network: containers start on network "none", or on a dedicated
//     egress network that is disconnected before any patched code runs
//   - non-root: uid 10001, all Linux capabilities dropped, no-new-privileges,
//     never privileged
//   - read-only root filesystem; the only writable paths are size-capped
//     tmpfs mounts (/workspace for the code, /tmp for caches)
//   - resource limits: memory (no swap), CPU, PIDs (fork bombs), open files
//   - time limits: every command runs under coreutils `timeout`, and the whole
//     validation under a context deadline
//   - no host mounts: code is streamed in as a tar archive over the Docker
//     API, so the sandbox never sees a host path
//   - always torn down: removal is deferred with a fresh context (so it runs
//     even on timeout), and a reaper deletes anything a crashed runner left
package sandbox

import (
	"fmt"

	"github.com/docker/docker/api/types/container"
)

// Label marks every sandbox container so the reaper can find them.
const Label = "devassist.sandbox"

// User is the unprivileged uid:gid every sandbox process runs as.
const User = "10001:10001"

// NetworkNone disables networking entirely.
const NetworkNone = "none"

// Limits caps a sandbox's resources.
type Limits struct {
	MemoryBytes int64
	NanoCPUs    int64 // 1e9 = one CPU
	PIDs        int64
	TmpfsBytes  int64 // size of each writable tmpfs mount
}

// DefaultLimits are deliberately modest; a test suite needing more should be
// configured explicitly.
var DefaultLimits = Limits{
	MemoryBytes: 1 << 30,
	NanoCPUs:    1_000_000_000,
	PIDs:        256,
	TmpfsBytes:  1 << 30,
}

// Spec describes one sandbox container.
type Spec struct {
	Image   string
	Network string // NetworkNone or the egress network's name
	Limits  Limits
	Labels  map[string]string
	Env     []string
}

// ContainerConfig is the process-level configuration.
func ContainerConfig(s Spec) *container.Config {
	labels := map[string]string{Label: "true"}
	for k, v := range s.Labels {
		labels[k] = v
	}
	return &container.Config{
		Image:      s.Image,
		User:       User,
		WorkingDir: "/workspace",
		// The container idles; every real command arrives via exec so each
		// one gets its own timeout and captured output.
		Cmd:             []string{"sleep", "infinity"},
		Env:             s.Env,
		Labels:          labels,
		NetworkDisabled: s.Network == NetworkNone,
	}
}

// HostConfig is where the isolation lives. Keep it boring and explicit.
func HostConfig(s Spec) *container.HostConfig {
	init := true
	pids := s.Limits.PIDs
	// Size in plain bytes: the kernel's tmpfs rejects "1GiB"-style units.
	// No mode= here; Docker sets 1777 on /tmp itself and duplicates fail.
	tmpfs := fmt.Sprintf("rw,exec,nosuid,nodev,size=%d,uid=10001,gid=10001", s.Limits.TmpfsBytes)
	return &container.HostConfig{
		NetworkMode:    container.NetworkMode(s.Network),
		ReadonlyRootfs: true,
		Privileged:     false,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		Init:           &init, // tini reaps zombies and forwards signals
		IpcMode:        "private",
		Tmpfs: map[string]string{
			"/workspace": tmpfs,
			"/tmp":       tmpfs,
		},
		Resources: container.Resources{
			Memory:     s.Limits.MemoryBytes,
			MemorySwap: s.Limits.MemoryBytes, // equal to Memory: no swap
			NanoCPUs:   s.Limits.NanoCPUs,
			PidsLimit:  &pids,
			Ulimits: []*container.Ulimit{
				{Name: "nofile", Soft: 1024, Hard: 1024},
				{Name: "nproc", Soft: 512, Hard: 512},
			},
		},
		LogConfig: container.LogConfig{Type: "none"},
	}
}
