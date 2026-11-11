package selfupdate

import (
	"context"
	"errors"
	"io"
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
			got, err := checkLatest(context.Background(), test.current, true, func(_ context.Context, name string, args ...string) ([]byte, error) {
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
			got, err := checkLatest(context.Background(), version, true, func(context.Context, string, ...string) ([]byte, error) {
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
	_, err := checkLatest(context.Background(), "v0.1.0", true, func(context.Context, string, ...string) ([]byte, error) {
		return []byte(" \n"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "empty version") {
		t.Fatalf("expected empty version error, got %v", err)
	}
}

func TestCheckLatestPreservesRunnerError(t *testing.T) {
	want := errors.New("proxy unavailable")
	_, err := checkLatest(context.Background(), "v0.1.0", true, func(context.Context, string, ...string) ([]byte, error) {
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

func TestCheckLatestSkipsPrebuiltReleaseBuilds(t *testing.T) {
	for _, current := range []string{"v0.4.1", "0.4.1"} {
		t.Run(current, func(t *testing.T) {
			called := false
			got, err := checkLatest(context.Background(), current, false, func(context.Context, string, ...string) ([]byte, error) {
				called = true
				return []byte("v0.5.0\n"), nil
			})
			if err != nil || got != "" || called {
				t.Fatalf("expected prebuilt build to skip check, current=%q got=%q called=%v err=%v", current, got, called, err)
			}
		})
	}
}

func TestCheckLatestExportedSkipsWhenNotGoInstall(t *testing.T) {
	oldCurrentVersion, oldIsGoInstall := currentVersion, isGoInstall
	t.Cleanup(func() {
		currentVersion, isGoInstall = oldCurrentVersion, oldIsGoInstall
	})
	currentVersion = func() string { return "v0.4.1" }
	isGoInstall = func() bool { return false }
	t.Setenv("PATH", t.TempDir())

	got, err := CheckLatest(context.Background())
	if err != nil || got != "" {
		t.Fatalf("expected CheckLatest to skip prebuilt release without invoking go, got=%q err=%v", got, err)
	}
}

func TestInstallLatestRejectsPrebuiltReleaseBuilds(t *testing.T) {
	for _, current := range []string{"v0.4.1", "v0.5.0-rc.1"} {
		t.Run(current, func(t *testing.T) {
			oldCurrentVersion, oldIsGoInstall, oldIsPrebuiltRelease, oldRunInstall := currentVersion, isGoInstall, isPrebuiltRelease, runInstall
			t.Cleanup(func() {
				currentVersion, isGoInstall, isPrebuiltRelease, runInstall = oldCurrentVersion, oldIsGoInstall, oldIsPrebuiltRelease, oldRunInstall
			})

			called := false
			currentVersion = func() string { return current }
			isGoInstall = func() bool { return false }
			isPrebuiltRelease = func() bool { return true }
			runInstall = func(context.Context, io.Writer, io.Writer) error {
				called = true
				return nil
			}

			var stdout, stderr strings.Builder
			err := InstallLatest(context.Background(), &stdout, &stderr)
			if err == nil {
				t.Fatal("expected error for prebuilt release build, got nil")
			}
			if called {
				t.Fatal("expected go install not to run for prebuilt release build")
			}
			for _, want := range []string{
				"prebuilt release",
				"package manager",
				"https://github.com/mathwro/pim-manager/releases/latest",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("expected error %q to contain %q", err.Error(), want)
				}
			}
		})
	}
}

func TestInstallLatestRejectsTaggedNonGoInstallBuild(t *testing.T) {
	oldCurrentVersion, oldIsGoInstall, oldIsPrebuiltRelease, oldRunInstall := currentVersion, isGoInstall, isPrebuiltRelease, runInstall
	t.Cleanup(func() {
		currentVersion, isGoInstall, isPrebuiltRelease, runInstall = oldCurrentVersion, oldIsGoInstall, oldIsPrebuiltRelease, oldRunInstall
	})

	called := false
	currentVersion = func() string { return "v0.4.1" }
	isGoInstall = func() bool { return false }
	isPrebuiltRelease = func() bool { return false }
	runInstall = func(context.Context, io.Writer, io.Writer) error {
		called = true
		return nil
	}

	err := InstallLatest(context.Background(), &strings.Builder{}, &strings.Builder{})
	if err == nil || called {
		t.Fatalf("expected tagged non-go-install build to be rejected without running go install, err=%v called=%v", err, called)
	}
}

func TestInstallLatestAllowsGoInstallAndDevBuilds(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		goInstall bool
	}{
		{name: "go install tagged build", current: "v0.4.1", goInstall: true},
		{name: "development build", current: "", goInstall: false},
		{name: "pseudo-version module build", current: "v0.4.2-0.20260717150525-6802e8aa589c", goInstall: false},
		{name: "dirty module build", current: "v0.4.1+dirty", goInstall: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldCurrentVersion, oldIsGoInstall, oldRunInstall := currentVersion, isGoInstall, runInstall
			t.Cleanup(func() {
				currentVersion, isGoInstall, runInstall = oldCurrentVersion, oldIsGoInstall, oldRunInstall
			})

			called := false
			currentVersion = func() string { return test.current }
			isGoInstall = func() bool { return test.goInstall }
			runInstall = func(context.Context, io.Writer, io.Writer) error {
				called = true
				return nil
			}

			var stdout, stderr strings.Builder
			if err := InstallLatest(context.Background(), &stdout, &stderr); err != nil {
				t.Fatalf("InstallLatest returned unexpected error: %v", err)
			}
			if !called {
				t.Fatal("expected go install runner to be called")
			}
			if !strings.Contains(stdout.String(), "Installed the latest tagged pim-manager version.") {
				t.Fatalf("expected install message on stdout, got %q", stdout.String())
			}
		})
	}
}
