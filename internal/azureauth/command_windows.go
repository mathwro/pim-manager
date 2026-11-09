package azureauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func newCLICommand(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	ext := filepath.Ext(path)
	if !strings.EqualFold(ext, ".cmd") && !strings.EqualFold(ext, ".bat") {
		return exec.CommandContext(ctx, path, args...), nil
	}
	line, err := batchCommandLine(path, args)
	if err != nil {
		return nil, err
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		return nil, errors.New("SystemRoot is required to run Azure CLI batch launchers")
	}
	command := exec.CommandContext(ctx, filepath.Join(root, "System32", "cmd.exe"))
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /v:off /s /c "%PIM_MANAGER_AZ_COMMAND%"`}
	command.Env = append(command.Environ(), "PIM_MANAGER_AZ_COMMAND="+line)
	// Killing cmd.exe alone leaves Python running and its inherited output pipes open.
	command.Cancel = func() error {
		kill := exec.Command(filepath.Join(root, "System32", "taskkill.exe"), "/pid", strconv.Itoa(command.Process.Pid), "/t", "/f")
		if err := kill.Run(); err != nil {
			return command.Process.Kill()
		}
		return nil
	}
	command.WaitDelay = time.Second
	return command, nil
}
