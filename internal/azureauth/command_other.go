//go:build !windows

package azureauth

import (
	"context"
	"os/exec"
)

func newCLICommand(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, name, args...), nil
}
