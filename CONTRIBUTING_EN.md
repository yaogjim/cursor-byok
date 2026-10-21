# Contributing Guide

> 中文版本：[CONTRIBUTING.md](./CONTRIBUTING.md)

Thank you for considering contributing to cursor-byok!

## Prerequisites

| Dependency | Version |
|------------|---------|
| Go | >= 1.25 |
| Node.js | >= 20 |
| Yarn | 1.x (classic) |
| [Task](https://taskfile.dev) | >= 3 |
| [Wails v3 CLI](https://v3alpha.wails.dev) | alpha.74+ |

Additional Linux dependencies: `libgtk-3-dev`, `libwebkit2gtk-4.1-dev` (required by Wails runtime).

## Quick Start

```bash
# Install frontend dependencies
cd frontend && yarn install --frozen-lockfile && cd ..

# Start dev mode (hot reload)
task dev

# Build for current platform
task build
```

## Project Structure

```
├── main.go                 # Entry point
├── internal/               # Go backend (proxy, forwarding, client management)
├── frontend/               # Vue 3 + Vite + Tailwind frontend
│   ├── src/
│   │   ├── views/          # Pages
│   │   ├── components/     # Components
│   │   ├── i18n/           # Internationalization (zh-CN / en-US / ja-JP / ru-RU)
│   │   └── state/          # Global state
│   └── plugins/            # Vite plugins (i18n static scanner, etc.)
├── prompt/                 # Built-in agent prompt templates
├── proto/                  # Protobuf definitions
├── build/                  # Build configs & platform Taskfiles
├── scripts/                # Helper scripts (release, metrics)
└── Taskfile.yml            # Top-level task orchestration
```

## Development Guidelines

### Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(proxy): support custom upstream timeout
fix(i18n): add missing Japanese translation keys
release: 0.0.42
```

### Code Style

- Go: follow `gofmt` / `go vet`; no additional linter config.
- Frontend: Vue SFC + Composition API, Tailwind utility-first.
- New UI strings must be added to ALL locale files (`frontend/src/i18n/locales/`).

### Branching & PRs

1. Create feature branches from `main`: `feat/xxx`, `fix/xxx`.
2. Keep PRs small and focused — one problem per PR.
3. Describe motivation and how to test in the PR description.

## Build & Release

Installers are built by GitHub Actions on hosted macOS / Windows / Linux runners; no local build upload is required.

```bash
# Trigger a three-platform build manually (or use "Run workflow" on GitHub)
gh workflow run build.yml

# Release: bump build/config.yml info.version and release-notes.md, then push a tag
git tag v1.78.0
git push origin v1.78.0
```

- Pushing to `main` / `gateway` builds macOS (arm64 + amd64 DMG and updater tar.gz), Windows (ZIP and NSIS installer) and Linux (updater tar.gz plus deb/rpm), uploaded as workflow artifacts.
- Pushing a `v*` tag publishes those artifacts to GitHub Releases together with a generated `update.json`. The tag must match `v` + `build/config.yml` `info.version`.
- Build one platform locally with `task release:prepare:macos`, `task release:build:windows:amd64`, `task release:build:windows:installer`, `task release:build:linux:amd64` or `task release:build:linux:installers` (deb/rpm needs `nfpm` installed).
- Tool versions (wails3, Node, yarn, nfpm, protoc plugins) are pinned at the top of `.github/workflows/build.yml`.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](./LICENSE).