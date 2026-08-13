package version

import "testing"

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
