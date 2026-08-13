package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type metadata struct {
	Owner                  string `json:"owner"`
	Repository             string `json:"repository"`
	Package                string `json:"package"`
	Binary                 string `json:"binary"`
	Description            string `json:"description"`
	Homepage               string `json:"homepage"`
	License                string `json:"license"`
	DistributionRepository string `json:"distributionRepository"`
	DistributionEvent      string `json:"distributionEvent"`
}

type target struct {
	goos, goarch, format string
}

var targets = []target{
	{goos: "windows", goarch: "amd64", format: "zip"},
	{goos: "windows", goarch: "arm64", format: "zip"},
	{goos: "darwin", goarch: "amd64", format: "tar.gz"},
	{goos: "darwin", goarch: "arm64", format: "tar.gz"},
	{goos: "linux", goarch: "amd64", format: "tar.gz"},
	{goos: "linux", goarch: "arm64", format: "tar.gz"},
}

const stableTagPattern = `^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: go run ./release <build|verify|smoke|metadata> [options]")
	}
	var err error
	switch os.Args[1] {
	case "build":
		err = build(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "smoke":
		err = smoke(os.Args[2:])
	case "metadata":
		err = printMetadata(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func build(args []string) error {
	flags := flag.NewFlagSet("build", flag.ContinueOnError)
	tag := flags.String("tag", "", "release tag (vMAJOR.MINOR.PATCH)")
	output := flags.String("output", "dist", "staging directory")
	commit := flags.String("commit", "", "full commit SHA")
	epoch := flags.Int64("source-date-epoch", 0, "tagged commit time as Unix seconds")
	signDarwin := flags.Bool("sign-darwin", false, "ad-hoc sign macOS binaries with codesign")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !stableTag(*tag) {
		return fmt.Errorf("tag %q must match %s", *tag, stableTagPattern)
	}
	if *commit == "" || *epoch <= 0 {
		return errors.New("-commit and positive -source-date-epoch are required")
	}
	meta, err := loadMetadata()
	if err != nil {
		return err
	}
	version := strings.TrimPrefix(*tag, "v")
	stamp := time.Unix(*epoch, 0).UTC()
	if err := os.RemoveAll(*output); err != nil {
		return fmt.Errorf("clear staging directory: %w", err)
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	for _, item := range targets {
		if err := buildTarget(meta, item, version, *commit, stamp, *output, *signDarwin); err != nil {
			return err
		}
	}
	if err := writeChecksums(*output); err != nil {
		return err
	}
	return verifyDir(meta, version, *output)
}

func buildTarget(meta metadata, item target, version, commit string, stamp time.Time, output string, signDarwin bool) error {
	suffix := ""
	if item.goos == "windows" {
		suffix = ".exe"
	}
	work, err := os.MkdirTemp("", "pim-manager-release-")
	if err != nil {
		return fmt.Errorf("create target staging directory: %w", err)
	}
	defer os.RemoveAll(work)
	binaryPath := filepath.Join(work, meta.Binary+suffix)
	ldflags := strings.Join([]string{
		"-s", "-w",
		"-X", "github.com/mathwro/pim-manager/internal/version.semanticVersion=" + version,
		"-X", "github.com/mathwro/pim-manager/internal/version.commit=" + commit,
		"-X", "github.com/mathwro/pim-manager/internal/version.buildDate=" + stamp.Format(time.DateOnly),
	}, " ")
	command := exec.Command("go", "build", "-mod=readonly", "-trimpath", "-ldflags", ldflags, "-o", binaryPath, ".")
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+item.goos, "GOARCH="+item.goarch, "SOURCE_DATE_EPOCH="+strconv.FormatInt(stamp.Unix(), 10))
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build %s_%s: %w", item.goos, item.goarch, err)
	}
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		return fmt.Errorf("set executable mode for %s_%s: %w", item.goos, item.goarch, err)
	}
	if signDarwin && item.goos == "darwin" {
		if runtime.GOOS != "darwin" {
			return errors.New("-sign-darwin requires a macOS build host")
		}
		command := exec.Command("codesign", "--force", "--sign", "-", "--timestamp=none", binaryPath)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("ad-hoc sign %s_%s: %w", item.goos, item.goarch, err)
		}
	}
	archive := filepath.Join(output, fmt.Sprintf("%s_%s_%s_%s.%s", meta.Package, version, item.goos, item.goarch, item.format))
	if item.format == "zip" {
		return writeZip(archive, binaryPath, meta.Binary+suffix, stamp)
	}
	return writeTarGz(archive, binaryPath, meta.Binary, stamp)
}

func writeZip(destination, source, name string, stamp time.Time) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o755)
	header.SetModTime(stamp)
	entry, err := writer.CreateHeader(header)
	if err == nil {
		_, err = io.CopyN(entry, sourceFile, info.Size())
	}
	return errors.Join(err, writer.Close(), file.Close())
}

func writeTarGz(destination, source, name string, stamp time.Time) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		file.Close()
		return err
	}
	gzipWriter.Header.ModTime = stamp
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{Name: name, Mode: 0o755, Size: info.Size(), ModTime: stamp, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := tarWriter.WriteHeader(header); err == nil {
		_, err = io.CopyN(tarWriter, sourceFile, info.Size())
	}
	return errors.Join(err, tarWriter.Close(), gzipWriter.Close(), file.Close())
}

func writeChecksums(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "checksums.txt" {
			continue
		}
		digest, err := digestFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return err
		}
		lines = append(lines, digest+"  "+entry.Name())
	}
	return os.WriteFile(filepath.Join(directory, "checksums.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	version := flags.String("version", "", "semantic version without v")
	directory := flags.String("dir", "dist", "staging directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !stableTag("v" + *version) {
		return fmt.Errorf("version %q is not stable SemVer", *version)
	}
	meta, err := loadMetadata()
	if err != nil {
		return err
	}
	return verifyDir(meta, *version, *directory)
}

func smoke(args []string) error {
	flags := flag.NewFlagSet("smoke", flag.ContinueOnError)
	version := flags.String("version", "", "semantic version without v")
	directory := flags.String("dir", "dist", "staging directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !stableTag("v" + *version) {
		return fmt.Errorf("version %q is not stable SemVer", *version)
	}
	meta, err := loadMetadata()
	if err != nil {
		return err
	}
	item := target{goos: runtime.GOOS, goarch: runtime.GOARCH, format: "tar.gz"}
	if runtime.GOOS == "windows" {
		item.format = "zip"
	}
	archive := filepath.Join(*directory, fmt.Sprintf("%s_%s_%s_%s.%s", meta.Package, *version, item.goos, item.goarch, item.format))
	work, err := os.MkdirTemp("", "pim-manager-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	executable := filepath.Join(work, meta.Binary)
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if err := extractExecutable(archive, executable, item); err != nil {
		return err
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		return err
	}
	for _, argument := range []string{"--version", "--help"} {
		command := exec.Command(executable, argument)
		output, err := command.Output()
		if err != nil {
			return fmt.Errorf("%s %s: %w", executable, argument, err)
		}
		if argument == "--version" && (!strings.Contains(string(output), *version) || strings.Count(string(output), "\n") != 1) {
			return fmt.Errorf("version output %q does not contain exactly one line with %s", output, *version)
		}
	}
	return nil
}

func extractExecutable(archive, destination string, item target) error {
	if item.format == "zip" {
		reader, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer reader.Close()
		if len(reader.File) != 1 {
			return errors.New("native ZIP does not contain exactly one executable")
		}
		source, err := reader.File[0].Open()
		if err != nil {
			return err
		}
		defer source.Close()
		return copyFile(destination, source)
	}
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	if _, err := tarReader.Next(); err != nil {
		return err
	}
	return copyFile(destination, tarReader)
}

func copyFile(destination string, source io.Reader) error {
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, source)
	return errors.Join(copyErr, file.Close())
}

func verifyDir(meta metadata, version, directory string) error {
	expected := make(map[string]target, len(targets))
	for _, item := range targets {
		name := fmt.Sprintf("%s_%s_%s_%s.%s", meta.Package, version, item.goos, item.goarch, item.format)
		expected[name] = item
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != len(expected)+1 {
		return fmt.Errorf("staging directory has %d entries, want %d", len(entries), len(expected)+1)
	}
	for _, entry := range entries {
		if entry.Name() == "checksums.txt" {
			continue
		}
		item, ok := expected[entry.Name()]
		if !ok || entry.IsDir() {
			return fmt.Errorf("unexpected release asset %q", entry.Name())
		}
		if err := inspectArchive(filepath.Join(directory, entry.Name()), meta.Binary, item); err != nil {
			return fmt.Errorf("inspect %s: %w", entry.Name(), err)
		}
		delete(expected, entry.Name())
	}
	if len(expected) != 0 {
		return fmt.Errorf("missing release assets: %v", sortedKeys(expected))
	}
	return verifyChecksums(directory)
}

func inspectArchive(filename, binary string, item target) error {
	expected := binary
	if item.goos == "windows" {
		expected += ".exe"
	}
	if item.format == "zip" {
		reader, err := zip.OpenReader(filename)
		if err != nil {
			return err
		}
		defer reader.Close()
		if len(reader.File) != 1 {
			return fmt.Errorf("contains %d entries, want 1", len(reader.File))
		}
		entry := reader.File[0]
		if err := safeArchivePath(entry.Name); err != nil {
			return err
		}
		if entry.Name != expected || entry.FileInfo().IsDir() || entry.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("contains invalid root executable %q mode %v", entry.Name, entry.Mode())
		}
		if entry.UncompressedSize64 == 0 {
			return errors.New("root executable is empty")
		}
		return nil
	}
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	header, err := tarReader.Next()
	if err != nil {
		return err
	}
	if err := safeArchivePath(header.Name); err != nil {
		return err
	}
	if header.Name != expected || header.Typeflag != tar.TypeReg || header.Mode&0o111 == 0 || header.Size == 0 {
		return fmt.Errorf("invalid root executable %q mode %#o size %d", header.Name, header.Mode, header.Size)
	}
	if _, err := io.Copy(io.Discard, tarReader); err != nil {
		return err
	}
	if _, err := tarReader.Next(); !errors.Is(err, io.EOF) {
		return errors.New("archive contains unexpected additional entries")
	}
	return nil
}

func safeArchivePath(name string) error {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean != name || clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("unsafe archive path %q", name)
	}
	return nil
}

func verifyChecksums(directory string) error {
	contents, err := os.ReadFile(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		return err
	}
	if strings.Contains(string(contents), "\r") || len(contents) == 0 || contents[len(contents)-1] != '\n' {
		return errors.New("checksums.txt must be nonempty UTF-8 with LF endings")
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	names := make([]string, 0, len(lines))
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 || len(parts[0]) != sha256.Size*2 {
			return fmt.Errorf("invalid checksum line %q", line)
		}
		if _, err := hex.DecodeString(parts[0]); err != nil || strings.ToLower(parts[0]) != parts[0] {
			return fmt.Errorf("invalid SHA-256 digest in %q", line)
		}
		if parts[1] == "checksums.txt" || seen[parts[1]] {
			return fmt.Errorf("invalid duplicate or self checksum for %q", parts[1])
		}
		actual, err := digestFile(filepath.Join(directory, parts[1]))
		if err != nil {
			return fmt.Errorf("checksum target %q: %w", parts[1], err)
		}
		if actual != parts[0] {
			return fmt.Errorf("checksum mismatch for %q", parts[1])
		}
		names = append(names, parts[1])
		seen[parts[1]] = true
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if !sort.StringsAreSorted(names) {
		return errors.New("checksums.txt is not sorted by filename")
	}
	if len(seen) != len(entries)-1 {
		return fmt.Errorf("checksums cover %d assets, want %d", len(seen), len(entries)-1)
	}
	return nil
}

func digestFile(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func printMetadata(args []string) error {
	flags := flag.NewFlagSet("metadata", flag.ContinueOnError)
	field := flags.String("field", "", "metadata JSON field")
	if err := flags.Parse(args); err != nil {
		return err
	}
	contents, err := os.ReadFile(filepath.Join("release", "metadata.json"))
	if err != nil {
		return err
	}
	var values map[string]string
	if err := json.Unmarshal(contents, &values); err != nil {
		return err
	}
	value, ok := values[*field]
	if !ok || value == "" {
		return fmt.Errorf("metadata field %q is missing", *field)
	}
	fmt.Println(value)
	return nil
}

func loadMetadata() (metadata, error) {
	var value metadata
	contents, err := os.ReadFile(filepath.Join("release", "metadata.json"))
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(contents, &value); err != nil {
		return value, err
	}
	if value.Owner == "" || value.Repository == "" || value.Package == "" || value.Binary == "" || value.Description == "" || value.Homepage == "" || value.License == "" || value.DistributionRepository == "" || value.DistributionEvent == "" {
		return value, errors.New("release metadata contains an empty required value")
	}
	return value, nil
}

func stableTag(tag string) bool {
	if !strings.HasPrefix(tag, "v") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func sortedKeys(values map[string]target) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	if runtime.GOOS == "windows" {
		os.Exit(1)
	}
	os.Exit(1)
}
