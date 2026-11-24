#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9a-zA-Z.-]+)?$ ]]; then
    printf 'Usage: %s vMAJOR.MINOR.PATCH [output directory]\n' "$0" >&2
    exit 2
fi

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p -- "${2:-"$project_dir/dist"}"
output_dir="$(cd -- "${2:-"$project_dir/dist"}" && pwd)"
build_dir="$(mktemp -d)"
trap 'rm -rf -- "$build_dir"' EXIT
cd -- "$project_dir"

assets=()
for os in linux darwin windows; do
    for arch in amd64 arm64; do
        name="meting-${version}-${os}-${arch}"
        stage="$build_dir/$name"
        executable="meting"
        mkdir -p -- "$stage"
        if [[ "$os" == windows ]]; then
            executable="meting.exe"
        fi
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
            -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$version" \
            -o "$stage/$executable" ./cmd/meting
        cp LICENSE NOTICE.md README.md README.zh-CN.md "$stage/"
        mkdir -p -- "$stage/docs"
        cp docs/IMPLEMENTATION.md docs/MIGRATE_NODE_TO_GO.md docs/site-gateway.example.json "$stage/docs/"
        if [[ "$os" == windows ]]; then
            (cd -- "$build_dir" && zip -qr "$output_dir/$name.zip" "$name")
            assets+=("$name.zip")
        else
            tar -C "$build_dir" -czf "$output_dir/$name.tar.gz" "$name"
            assets+=("$name.tar.gz")
        fi
    done
done

cd -- "$output_dir"
if command -v sha256sum >/dev/null; then
    sha256sum "${assets[@]}" > SHA256SUMS
else
    shasum -a 256 "${assets[@]}" > SHA256SUMS
fi
