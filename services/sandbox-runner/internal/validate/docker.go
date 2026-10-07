package validate

import (
	"context"

	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/sandbox"
)

// FromDocker adapts *sandbox.Docker to the Sandbox interface.
func FromDocker(d *sandbox.Docker) Sandbox { return dockerSandbox{d} }

type dockerSandbox struct{ d *sandbox.Docker }

func (s dockerSandbox) Start(ctx context.Context, spec sandbox.Spec) (Box, error) {
	return s.d.Start(ctx, spec)
}

func (s dockerSandbox) ImageExists(ctx context.Context, image string) (bool, error) {
	return s.d.ImageExists(ctx, image)
}
