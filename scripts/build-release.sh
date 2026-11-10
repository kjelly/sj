#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 2 || ! $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]]; then
  echo 'usage: scripts/build-release.sh vMAJOR.MINOR.PATCH OUTPUT_DIRECTORY' >&2
  exit 2
fi
version=$1
mkdir -p "$2"
output=$(cd "$2" && pwd)
root=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/sj-release.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
cd "$root"

for arch in amd64 arm64; do
  package="sj_${version}_linux_${arch}"
  mkdir -p "$scratch/$package/docs"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOMAXPROCS="${GOMAXPROCS:-2}" \
    go build -p 2 -trimpath -ldflags='-s -w' -o "$scratch/$package/sj" ./cmd/sj
  cp README.md "$scratch/$package/"
  cp docs/cli.md docs/architecture.md "$scratch/$package/docs/"
  tar -C "$scratch" -czf "$output/$package.tar.gz" "$package"
  rm -rf -- "$scratch/$package"
done
(
  cd "$output"
  sha256sum "sj_${version}_linux_amd64.tar.gz" "sj_${version}_linux_arm64.tar.gz" > SHA256SUMS
)
