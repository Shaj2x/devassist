package sandbox

import (
	"slices"
	"strings"
	"testing"
)

// These assertions are the security contract of the sandbox. If one fails,
// someone weakened isolation; that should be a deliberate, reviewed change.
func TestHostConfigIsLockedDown(t *testing.T) {
	hc := HostConfig(Spec{Image: "x", Network: NetworkNone, Limits: DefaultLimits})

	if !hc.ReadonlyRootfs {
		t.Error("root filesystem must be read-only")
	}
	if hc.Privileged {
		t.Error("must never be privileged")
	}
	if !slices.Equal(hc.CapDrop, []string{"ALL"}) || len(hc.CapAdd) != 0 {
		t.Errorf("capabilities: drop=%v add=%v", hc.CapDrop, hc.CapAdd)
	}
	if !slices.Contains(hc.SecurityOpt, "no-new-privileges") {
		t.Error("no-new-privileges missing")
	}
	if hc.NetworkMode != "none" {
		t.Errorf("network = %s", hc.NetworkMode)
	}
	if hc.Memory != 1<<30 || hc.MemorySwap != hc.Memory {
		t.Errorf("memory=%d swap=%d (swap must equal memory)", hc.Memory, hc.MemorySwap)
	}
	if hc.NanoCPUs == 0 || hc.PidsLimit == nil || *hc.PidsLimit != 256 {
		t.Error("cpu and pid limits required")
	}
	if len(hc.Binds) != 0 || len(hc.Mounts) != 0 {
		t.Error("no host mounts allowed")
	}
	for path, opts := range hc.Tmpfs {
		if !strings.Contains(opts, "size=") || !strings.Contains(opts, "nosuid") || !strings.Contains(opts, "nodev") {
			t.Errorf("tmpfs %s lacks size/nosuid/nodev: %s", path, opts)
		}
	}
}

func TestContainerConfigRunsAsNonRoot(t *testing.T) {
	cfg := ContainerConfig(Spec{Image: "img", Network: NetworkNone, Labels: map[string]string{"devassist.patch_id": "p1"}})
	if cfg.User != "10001:10001" {
		t.Errorf("user = %q", cfg.User)
	}
	if !cfg.NetworkDisabled {
		t.Error("network should be disabled")
	}
	if cfg.Labels[Label] != "true" || cfg.Labels["devassist.patch_id"] != "p1" {
		t.Errorf("labels = %v", cfg.Labels)
	}
	if egress := ContainerConfig(Spec{Network: "devassist-sandbox-egress"}); egress.NetworkDisabled {
		t.Error("egress network spec should not disable networking")
	}
}
