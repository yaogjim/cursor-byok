#!/usr/bin/env bash

set -Eeuo pipefail

usage() {
  cat <<'EOF'
在本地 bin/release/<version>/ 生成 release 资产，不执行 GitHub 发布。

用法：
  ./scripts/build-release.sh [选项]

选项：
  --macos    只生成 macOS arm64、macOS amd64（默认）
  --all      生成 macOS arm64、macOS amd64、Windows amd64、Linux amd64
  --dry-run  只显示将执行的 Task，不编译
  -h, --help 显示帮助

示例：
  ./scripts/build-release.sh
  ./scripts/build-release.sh --all
  ./scripts/build-release.sh --all --dry-run
EOF
}

fail() {
  printf 'build-release: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "未检测到 $1，请先安装后再执行"
}

mode="macos"
dry_run="false"

while (($# > 0)); do
  case "$1" in
    --macos)
      mode="macos"
      ;;
    --all)
      mode="all"
      ;;
    --dry-run)
      dry_run="true"
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "未知参数：$1（使用 --help 查看用法）"
      ;;
  esac
  shift
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_root="$(cd "${script_dir}/.." && pwd)"
cd "$project_root"

[[ "$(uname -s)" == "Darwin" ]] || fail "当前构建流程仅支持在 macOS 主机执行"
require_command go
require_command task

if [[ "$dry_run" == "false" ]]; then
  require_command wails3
  if [[ "$mode" == "all" ]]; then
    require_command docker
    require_command zip
    docker info >/dev/null 2>&1 || fail "Docker daemon 未运行；全平台模式需要 Docker 构建 Linux 包"
    docker image inspect cursor-linux-amd64-cross >/dev/null 2>&1 || fail "缺少 cursor-linux-amd64-cross 镜像，请先执行 task setup:docker"
  fi
fi

version="$(go run ./scripts/release version -config ./build/config.yml)"
[[ -n "$version" ]] || fail "build/config.yml 中的版本号为空"
release_dir="bin/release/${version}"

task_name="release:prepare:macos"
if [[ "$mode" == "all" ]]; then
  task_name="release:prepare:darwin"
fi

command=(task "$task_name")

printf '版本：%s\n' "$version"
printf '模式：%s\n' "$mode"
printf '输出：%s\n' "$release_dir"
printf '执行：'
printf '%q ' "${command[@]}"
printf '\n'

if [[ "$dry_run" == "true" ]]; then
  printf 'dry-run：仅预览，未执行编译。\n'
  exit 0
fi

"${command[@]}"

expected=(
  "cursor-byok-${version}-macos-arm64.tar.gz"
  "cursor-byok-${version}-macos-amd64.tar.gz"
)
if [[ "$mode" == "all" ]]; then
  expected+=(
    "cursor-byok-${version}-windows-amd64.zip"
    "cursor-byok-${version}-linux-amd64.tar.gz"
  )
fi

for filename in "${expected[@]}"; do
  [[ -f "${release_dir}/${filename}" ]] || fail "构建结束但缺少资产：${release_dir}/${filename}"
done
[[ -s "${release_dir}/.release-notes.md" ]] || fail "构建结束但缺少发布说明：${release_dir}/.release-notes.md"

printf '\nrelease 资产已生成：\n'
for filename in "${expected[@]}"; do
  printf '  %s\n' "${release_dir}/${filename}"
done