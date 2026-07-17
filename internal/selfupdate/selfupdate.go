package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime/debug"
	"strings"
)

const module = "github.com/mathwro/pim-manager"
const latestModule = module + "@latest"

type outputRunner func(context.Context, string, ...string) ([]byte, error)

func currentVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return strings.TrimSpace(info.Main.Version)
}

func stableTag(version string) bool {
	return strings.HasPrefix(version, "v") && !strings.Contains(version, "-")
}

func checkLatest(ctx context.Context, current string, run outputRunner) (string, error) {
	if !stableTag(current) {
		return "", nil
	}
	out, err := run(ctx, "go", "list", "-m", "-f={{.Version}}", latestModule)
	if err != nil {
		return "", fmt.Errorf("check latest pim-manager version: %w", err)
	}
	latest := strings.TrimSpace(string(out))
	if latest == "" {
		return "", errors.New("check latest pim-manager version: Go returned an empty version")
	}
	if latest == current {
		return "", nil
	}
	return latest, nil
}

func CheckLatest(ctx context.Context) (string, error) {
	return checkLatest(ctx, currentVersion(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	})
}

func InstallLatest(ctx context.Context, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, "go", "install", latestModule)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("update pim-manager: the Go toolchain is required: %w", err)
		}
		return fmt.Errorf("update pim-manager: %w", err)
	}
	_, err := fmt.Fprintln(stdout, "Installed the latest tagged pim-manager version. Restart pim-manager to use it.")
	return err
}
