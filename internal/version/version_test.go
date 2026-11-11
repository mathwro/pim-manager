package version

import (
	"runtime/debug"
	"testing"
)

func TestInjectedBuildMetadata(t *testing.T) {
	oldVersion, oldCommit, oldDate := semanticVersion, commit, buildDate
	t.Cleanup(func() {
		semanticVersion, commit, buildDate = oldVersion, oldCommit, oldDate
	})

	semanticVersion = "1.2.3"
	commit = "0123456789abcdef0123456789abcdef01234567"
	buildDate = "2026-08-13"

	if got, want := String(), "pim-manager 1.2.3 (commit 0123456789ab, built 2026-08-13)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := Tag(), "v1.2.3"; got != want {
		t.Fatalf("Tag() = %q, want %q", got, want)
	}
}

func TestDevelopmentMetadataHasNoUpdateTag(t *testing.T) {
	oldVersion, oldCommit, oldDate := semanticVersion, commit, buildDate
	t.Cleanup(func() {
		semanticVersion, commit, buildDate = oldVersion, oldCommit, oldDate
	})

	semanticVersion = ""
	commit = ""
	buildDate = ""

	if got := Tag(); got != "" {
		t.Fatalf("Tag() = %q, want empty development tag", got)
	}
}

func TestIsGoInstall(t *testing.T) {
	oldVersion, oldCommit, oldDate, oldReadBuildInfo := semanticVersion, commit, buildDate, readBuildInfo
	t.Cleanup(func() {
		semanticVersion, commit, buildDate, readBuildInfo = oldVersion, oldCommit, oldDate, oldReadBuildInfo
	})

	tests := []struct {
		name        string
		ldflagsVer  string
		mainVersion string
		ok          bool
		want        bool
		wantTag     string
	}{
		{
			name:        "go install tagged module build",
			ldflagsVer:  "",
			mainVersion: "v0.4.1",
			ok:          true,
			want:        true,
			wantTag:     "v0.4.1",
		},
		{
			name:        "prebuilt release build without v prefix",
			ldflagsVer:  "0.4.1",
			mainVersion: "(devel)",
			ok:          true,
			want:        false,
			wantTag:     "v0.4.1",
		},
		{
			name:        "prebuilt release build with v prefix",
			ldflagsVer:  "v0.4.1",
			mainVersion: "v0.4.1",
			ok:          true,
			want:        false,
			wantTag:     "v0.4.1",
		},
		{
			name:        "development build",
			ldflagsVer:  "",
			mainVersion: "(devel)",
			ok:          true,
			want:        false,
			wantTag:     "",
		},
		{
			name:        "pseudo-version module build",
			ldflagsVer:  "",
			mainVersion: "v0.4.2-0.20260717150525-6802e8aa589c",
			ok:          true,
			want:        false,
			wantTag:     "v0.4.2-0.20260717150525-6802e8aa589c",
		},
		{
			name:        "dirty module build",
			ldflagsVer:  "",
			mainVersion: "v0.4.1+dirty",
			ok:          true,
			want:        false,
			wantTag:     "v0.4.1+dirty",
		},
		{
			name:        "missing build info",
			ldflagsVer:  "",
			mainVersion: "",
			ok:          false,
			want:        false,
			wantTag:     "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			semanticVersion = test.ldflagsVer
			commit = ""
			buildDate = ""
			readBuildInfo = func() (*debug.BuildInfo, bool) {
				if !test.ok {
					return nil, false
				}
				return &debug.BuildInfo{
					Main: debug.Module{Version: test.mainVersion},
				}, true
			}

			if got := IsGoInstall(); got != test.want {
				t.Fatalf("IsGoInstall() = %v, want %v", got, test.want)
			}
			if got := Tag(); got != test.wantTag {
				t.Fatalf("Tag() = %q, want %q", got, test.wantTag)
			}
		})
	}
}

func TestIsPrebuiltRelease(t *testing.T) {
	oldVersion := semanticVersion
	t.Cleanup(func() {
		semanticVersion = oldVersion
	})

	for _, tc := range []struct {
		ldflagsVer string
		want       bool
	}{
		{ldflagsVer: "", want: false},
		{ldflagsVer: "   ", want: false},
		{ldflagsVer: "0.4.1", want: true},
		{ldflagsVer: "v0.4.1", want: true},
		{ldflagsVer: "v0.5.0-rc.1", want: true},
	} {
		semanticVersion = tc.ldflagsVer
		if got := IsPrebuiltRelease(); got != tc.want {
			t.Fatalf("IsPrebuiltRelease() with semanticVersion=%q = %v, want %v", tc.ldflagsVer, got, tc.want)
		}
	}
}
