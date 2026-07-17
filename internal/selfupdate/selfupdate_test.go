package selfupdate

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestCheckLatestComparesStableTags(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    string
	}{
		{name: "new stable tag", current: "v0.1.0", latest: "v0.1.1\n", want: "v0.1.1"},
		{name: "same stable tag", current: "v0.1.1", latest: "v0.1.1\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotName string
			var gotArgs []string
			got, err := checkLatest(context.Background(), test.current, func(_ context.Context, name string, args ...string) ([]byte, error) {
				gotName = name
				gotArgs = args
				return []byte(test.latest), nil
			})
			if err != nil {
				t.Fatalf("checkLatest returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
			if gotName != "go" || !reflect.DeepEqual(gotArgs, []string{"list", "-m", "-f={{.Version}}", "github.com/mathwro/pim-manager@latest"}) {
				t.Fatalf("unexpected command %q %#v", gotName, gotArgs)
			}
		})
	}
}

func TestCheckLatestSkipsUntaggedBuilds(t *testing.T) {
	for _, version := range []string{"", "(devel)", "v0.1.1-0.20260717150525-6802e8aa589c", "v0.2.0-beta.1"} {
		t.Run(version, func(t *testing.T) {
			called := false
			got, err := checkLatest(context.Background(), version, func(context.Context, string, ...string) ([]byte, error) {
				called = true
				return nil, nil
			})
			if err != nil || got != "" || called {
				t.Fatalf("expected skipped check, version=%q got=%q called=%v err=%v", version, got, called, err)
			}
		})
	}
}

func TestCheckLatestRejectsEmptyVersion(t *testing.T) {
	_, err := checkLatest(context.Background(), "v0.1.0", func(context.Context, string, ...string) ([]byte, error) {
		return []byte(" \n"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "empty version") {
		t.Fatalf("expected empty version error, got %v", err)
	}
}

func TestCheckLatestPreservesRunnerError(t *testing.T) {
	want := errors.New("proxy unavailable")
	_, err := checkLatest(context.Background(), "v0.1.0", func(context.Context, string, ...string) ([]byte, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

func TestInstallLatestReportsMissingGo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := InstallLatest(context.Background(), &strings.Builder{}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "Go toolchain is required") || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("expected missing Go guidance, got %v", err)
	}
}
