package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStableTag(t *testing.T) {
	for _, tag := range []string{"v0.1.0", "v1.2.3", "v10.20.30"} {
		if !stableTag(tag) {
			t.Errorf("stableTag(%q) = false", tag)
		}
	}
	for _, tag := range []string{"1.2.3", "v01.2.3", "v1.2", "v1.2.3-rc.1", "v1.2.3+build.7"} {
		if stableTag(tag) {
			t.Errorf("stableTag(%q) = true", tag)
		}
	}
}

func TestSafeArchivePath(t *testing.T) {
	for _, name := range []string{"pim-manager", "pim-manager.exe"} {
		if err := safeArchivePath(name); err != nil {
			t.Errorf("safeArchivePath(%q): %v", name, err)
		}
	}
	for _, name := range []string{"../pim-manager", "/pim-manager", "dir/../pim-manager", `..\pim-manager`, "."} {
		if err := safeArchivePath(name); err == nil {
			t.Errorf("safeArchivePath(%q) unexpectedly succeeded", name)
		}
	}
}

func TestArchiveInspectionRejectsUnexpectedLayout(t *testing.T) {
	directory := t.TempDir()
	zipName := filepath.Join(directory, "bad.zip")
	zipFile, err := os.Create(zipName)
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(zipFile)
	entry, err := zipWriter.Create("nested/pim-manager.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("binary")); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := inspectArchive(zipName, "pim-manager", target{goos: "windows", format: "zip"}); err == nil {
		t.Fatal("expected nested ZIP executable to be rejected")
	}
}

func TestChecksumsAreSortedAndIndependentlyVerified(t *testing.T) {
	directory := t.TempDir()
	for name, content := range map[string]string{"b.zip": "b", "a.tar.gz": "a"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeChecksums(directory); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if !strings.HasSuffix(lines[0], "  a.tar.gz") || !strings.HasSuffix(lines[1], "  b.zip") {
		t.Fatalf("unexpected checksum order: %q", contents)
	}
	if err := verifyChecksums(directory); err != nil {
		t.Fatalf("verifyChecksums: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "a.tar.gz"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksums(directory); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestTarGzPreservesRootExecutableMode(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "source")
	archive := filepath.Join(directory, "archive.tar.gz")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTarGz(archive, binary, "pim-manager", time.Unix(1_700_000_000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := inspectArchive(archive, "pim-manager", target{goos: "linux", format: "tar.gz"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	header, err := tar.NewReader(gzipReader).Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "pim-manager" || header.Mode != 0o755 {
		t.Fatalf("unexpected archive entry %q mode %#o", header.Name, header.Mode)
	}
}
