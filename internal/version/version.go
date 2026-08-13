package version

import (
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

const Binary = "pim-manager"

var (
	semanticVersion string
	commit          string
	buildDate       string
)

// String returns the non-mutating CLI version line.
func String() string {
	version, revision, date := buildMetadata()
	return fmt.Sprintf("%s %s (commit %s, built %s)", Binary, version, shortRevision(revision), date)
}

// Tag returns the module-style version tag for update checks. Development builds
// return an empty string so they never perform networked update checks.
func Tag() string {
	version, _, _ := buildMetadata()
	if version == "0.0.0-dev" {
		return ""
	}
	return "v" + strings.TrimPrefix(version, "v")
}

func buildMetadata() (string, string, string) {
	version := strings.TrimPrefix(strings.TrimSpace(semanticVersion), "v")
	revision := strings.TrimSpace(commit)
	date := strings.TrimSpace(buildDate)

	if info, ok := debug.ReadBuildInfo(); ok {
		if version == "" && strings.HasPrefix(info.Main.Version, "v") {
			version = strings.TrimPrefix(info.Main.Version, "v")
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if revision == "" {
					revision = setting.Value
				}
			case "vcs.time":
				if date == "" {
					if timestamp, err := time.Parse(time.RFC3339, setting.Value); err == nil {
						date = timestamp.UTC().Format(time.DateOnly)
					}
				}
			}
		}
	}

	if version == "" {
		version = "0.0.0-dev"
	}
	if revision == "" {
		revision = "unknown"
	}
	if date == "" {
		date = "unknown"
	}
	return version, revision, date
}

func shortRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
