//go:build windows

package azureauth

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestWindowsAzureCLILauncherCancellationStopsChild(t *testing.T) {
	installAzureCLILauncher(t)
	marker := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("PIM_TEST_AZ_WAIT", marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := execCommand(ctx, "az", "account", "list")
		finished <- err
	}()

	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var child *os.Process
	for child == nil {
		select {
		case err := <-finished:
			t.Fatalf("CLI exited before child startup: %v", err)
		case <-deadline:
			t.Fatal("CLI child did not start")
		case <-tick.C:
			data, err := os.ReadFile(marker)
			if os.IsNotExist(err) || len(data) == 0 {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			child, err = os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	defer child.Kill()
	childStopped := make(chan struct{})
	go func() {
		_, _ = child.Wait()
		close(childStopped)
	}()
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("canceled CLI succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation left CLI output pipes open")
	}
	select {
	case <-childStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation left launcher child running")
	}
}
