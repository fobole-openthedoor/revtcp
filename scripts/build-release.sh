#!/bin/bash
set -euo pipefail
project_dir=$(cd "$(dirname "$0")/.." && pwd)
cd "$project_dir"
release_version=${1:-dev}
[[ $# -le 1 && "$release_version" =~ ^[a-zA-Z0-9.+_-]+$ ]] || { printf 'Usage: build-release.sh [VERSION]\n' >&2; exit 1; }
export REVTCP_VERSION="$release_version"
export REVTCP_COMMIT=${REVTCP_COMMIT:-$(git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)}
export REVTCP_BUILD_TIME=${REVTCP_BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
[[ "$REVTCP_COMMIT" =~ ^[a-zA-Z0-9_-]+$ ]] || { printf 'Invalid commit metadata\n' >&2; exit 1; }
ldflags="-s -w -X main.version=$REVTCP_VERSION -X main.commit=$REVTCP_COMMIT -X main.buildTime=$REVTCP_BUILD_TIME"
output="$project_dir/dist/$release_version"
mkdir -p "$output" bin
stage_dir=$(mktemp -d "$project_dir/dist/release.XXXXXX")
trap 'rm -rf "$stage_dir"' EXIT

bash scripts/build-macos.sh
for arch in arm64 amd64; do
    cp "dist/revtcp-macos-$arch.tar.gz" "$output/revtcp-$release_version-macos-$arch.tar.gz"
done

for target in linux/amd64 linux/arm64 windows/amd64; do
    os=${target%/*}
    arch=${target#*/}
    name="revtcp-$os-$arch"
    executable=revtcp
    [[ "$os" != windows ]] || executable=revtcp.exe
    package_dir="$stage_dir/$name"
    mkdir -p "$package_dir/deploy/macos" "$package_dir/docs"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="$ldflags" -o "$package_dir/$executable" ./cmd/revtcp
    cp README.md "$package_dir/"
    cp docs/macos.md "$package_dir/docs/"
    cp deploy/macos/client.example.json "$package_dir/client.example.json"
    cp deploy/macos/client.example.json "$package_dir/deploy/macos/"
    cp "$package_dir/$executable" "bin/$name${executable#revtcp}"
    if [[ "$os" == windows ]]; then
        # Write a fresh zip so reruns cannot retain files from an older build.
        (cd "$stage_dir" && zip -qr "$stage_dir/$name.zip" "$name")
        mv "$stage_dir/$name.zip" "$output/revtcp-$release_version-$os-$arch.zip"
    else
        tar -czf "$output/revtcp-$release_version-$os-$arch.tar.gz" -C "$stage_dir" "$name"
    fi
done
(
    cd "$output"
    shasum -a 256 \
        "revtcp-$release_version-linux-amd64.tar.gz" \
        "revtcp-$release_version-linux-arm64.tar.gz" \
        "revtcp-$release_version-macos-amd64.tar.gz" \
        "revtcp-$release_version-macos-arm64.tar.gz" \
        "revtcp-$release_version-windows-amd64.zip" > SHA256SUMS
)
printf 'Release packages: %s\n' "$output"
