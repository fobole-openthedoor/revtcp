#!/bin/bash
set -euo pipefail
project_dir=$(cd "$(dirname "$0")/.." && pwd)
cd "$project_dir"
mkdir -p bin dist
release_version=${REVTCP_VERSION:-dev}
release_commit=${REVTCP_COMMIT:-$(git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)}
release_time=${REVTCP_BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
[[ "$release_version" =~ ^[a-zA-Z0-9.+_-]+$ && "$release_commit" =~ ^[a-zA-Z0-9_-]+$ ]] || { printf 'Invalid build metadata\n' >&2; exit 1; }
ldflags="-s -w -X main.version=$release_version -X main.commit=$release_commit -X main.buildTime=$release_time"
for arch in arm64 amd64; do
    name="revtcp-macos-$arch"
    CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath -ldflags="$ldflags" -o "bin/revtcp-darwin-$arch" ./cmd/revtcp
    package_dir=$(mktemp -d "$project_dir/dist/package.XXXXXX")
    trap 'rm -rf "$package_dir"' EXIT
    mkdir "$package_dir/$name"
    cp "bin/revtcp-darwin-$arch" "$package_dir/$name/revtcp"
    cp deploy/macos/service.sh deploy/macos/local.revtcp.client.plist deploy/macos/client.example.json "$package_dir/$name/"
    cp docs/macos.md "$package_dir/$name/MACOS.md"
    chmod 755 "$package_dir/$name/revtcp" "$package_dir/$name/service.sh"
    tar -czf "dist/$name.tar.gz" -C "$package_dir" "$name"
    rm -rf "$package_dir"
    trap - EXIT
done
printf 'Built dist/revtcp-macos-{arm64,amd64}.tar.gz\n'
