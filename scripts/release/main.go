package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type buildConfig struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
}

type updateManifest struct {
	Version      string                         `json:"version"`
	ReleaseDate  string                         `json:"release_date"`
	ReleaseNotes string                         `json:"release_notes"`
	Platforms    map[string]updateManifestAsset `json:"platforms"`
	Mandatory    bool                           `json:"mandatory"`
}

type updateManifestAsset struct {
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
}

type assetSpec struct {
	platform string
	suffix   string
}

var releaseAssets = []assetSpec{
	{platform: "macos-arm64", suffix: ".tar.gz"},
	{platform: "macos-amd64", suffix: ".tar.gz"},
	{platform: "windows-amd64", suffix: ".zip"},
	{platform: "linux-amd64", suffix: ".tar.gz"},
}

func main() {
	if len(os.Args) < 2 {
		exitf("usage: go run ./scripts/release <version|notes|manifest|zip> [flags]")
	}

	switch os.Args[1] {
	case "version":
		runVersion(os.Args[2:])
	case "notes":
		runNotes(os.Args[2:])
	case "manifest":
		runManifest(os.Args[2:])
	case "zip":
		runZip(os.Args[2:])
	default:
		exitf("unknown subcommand: %s", os.Args[1])
	}
}

func runVersion(args []string) {
	flags := flag.NewFlagSet("version", flag.ExitOnError)
	configPath := flags.String("config", "build/config.yml", "path to build config")
	_ = flags.Parse(args)

	version, err := readVersion(*configPath)
	if err != nil {
		exitErr(err)
	}

	fmt.Print(version)
}

// runZip 把目录内容打包成 zip 归档，供发布流程生成 Windows ZIP 包。
// CI 的 Windows 运行器上 zip 命令行为不一致（例如不支持 -y 选项），
// 因此改用 Go 实现，保证各平台产物一致。
func runZip(args []string) {
	flags := flag.NewFlagSet("zip", flag.ExitOnError)
	srcDir := flags.String("src", "", "directory whose contents become the archive root")
	outPath := flags.String("out", "", "path of the zip archive to write")
	_ = flags.Parse(args)

	if *srcDir == "" || *outPath == "" {
		exitf("zip requires both -src and -out")
	}
	if err := writeZipDir(*srcDir, *outPath); err != nil {
		exitErr(err)
	}
}

func writeZipDir(srcDir, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create zip parent dir: %w", err)
	}
	file, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create zip file: %w", err)
	}
	writer := zip.NewWriter(file)
	base := filepath.Clean(srcDir)
	walkErr := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if info.IsDir() {
			header.Name += "/"
			header.Method = zip.Store
			_, err := writer.CreateHeader(header)
			return err
		}
		header.Method = zip.Deflate
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		defer source.Close()
		_, err = io.Copy(entry, source)
		return err
	})
	if walkErr != nil {
		_ = writer.Close()
		_ = file.Close()
		return walkErr
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		return fmt.Errorf("finalize zip: %w", err)
	}
	return file.Close()
}

func runNotes(args []string) {
	flags := flag.NewFlagSet("notes", flag.ExitOnError)
	_ = flags.String("config", "build/config.yml", "path to build config")
	outputPath := flags.String("out", "", "output file path")
	sourcePath := flags.String("source", "", "source markdown file")
	_ = flags.Parse(args)

	if strings.TrimSpace(*outputPath) == "" {
		exitf("notes output path is required")
	}
	if strings.TrimSpace(*sourcePath) == "" {
		exitf("notes source path is required")
	}

	notes, err := resolveReleaseNotes(*sourcePath)
	if err != nil {
		exitErr(err)
	}

	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		exitErr(err)
	}
	if err := os.WriteFile(*outputPath, []byte(notes), 0o644); err != nil {
		exitErr(err)
	}
}

func runManifest(args []string) {
	flags := flag.NewFlagSet("manifest", flag.ExitOnError)
	configPath := flags.String("config", "build/config.yml", "path to build config")
	assetsDir := flags.String("assets-dir", "", "directory containing release assets")
	outputPath := flags.String("out", "", "manifest output file")
	repo := flags.String("repo", "", "GitHub repo in owner/repo form")
	baseName := flags.String("base-name", "cursor-byok", "release asset basename")
	notesPath := flags.String("notes", "", "release notes file")
	_ = flags.Parse(args)

	if strings.TrimSpace(*assetsDir) == "" {
		exitf("assets-dir is required")
	}
	if strings.TrimSpace(*outputPath) == "" {
		exitf("manifest output path is required")
	}
	if strings.TrimSpace(*repo) == "" {
		exitf("repo is required")
	}
	if strings.TrimSpace(*notesPath) == "" {
		exitf("notes is required")
	}

	version, err := readVersion(*configPath)
	if err != nil {
		exitErr(err)
	}

	notes, err := resolveReleaseNotes(*notesPath)
	if err != nil {
		exitErr(err)
	}

	manifest := updateManifest{
		Version:      version,
		ReleaseDate:  time.Now().UTC().Format(time.RFC3339),
		ReleaseNotes: notes,
		Platforms:    map[string]updateManifestAsset{},
		Mandatory:    false,
	}

	for _, spec := range releaseAssets {
		filename := fmt.Sprintf("%s-%s-%s%s", *baseName, version, spec.platform, spec.suffix)
		fullpath := filepath.Join(*assetsDir, filename)
		asset, err := buildManifestAsset(fullpath, *repo, version, filename)
		if err != nil {
			exitErr(err)
		}
		manifest.Platforms[spec.platform] = asset
	}

	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		exitErr(err)
	}

	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		exitErr(err)
	}
	content = append(content, '\n')

	if err := os.WriteFile(*outputPath, content, 0o644); err != nil {
		exitErr(err)
	}
}

func readVersion(configPath string) (string, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}

	var cfg buildConfig
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return "", err
	}

	version := strings.TrimSpace(strings.TrimPrefix(cfg.Info.Version, "v"))
	if version == "" {
		return "", errors.New("build/config.yml info.version is empty")
	}
	return version, nil
}

func resolveReleaseNotes(sourcePath string) (string, error) {
	candidate := strings.TrimSpace(sourcePath)
	if candidate == "" {
		return "", errors.New("release notes source path is required")
	}

	content, err := os.ReadFile(candidate)
	if err != nil {
		return "", err
	}

	notes := strings.TrimSpace(string(content))
	if notes == "" {
		return "", fmt.Errorf("release notes file %s is empty", candidate)
	}
	return notes, nil
}

func buildManifestAsset(path, repo, version, filename string) (updateManifestAsset, error) {
	info, err := os.Stat(path)
	if err != nil {
		return updateManifestAsset{}, err
	}

	checksum, err := sha256File(path)
	if err != nil {
		return updateManifestAsset{}, err
	}

	return updateManifestAsset{
		URL:      fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", repo, version, filename),
		Size:     info.Size(),
		Checksum: "sha256:" + checksum,
	}, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func exitErr(err error) {
	exitf("%v", err)
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
