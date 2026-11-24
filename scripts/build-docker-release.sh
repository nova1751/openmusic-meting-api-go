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
assets=()
for arch in amd64 arm64; do
    archive="meting-${version}-linux-${arch}.docker.tar.gz"
    docker buildx build --platform "linux/$arch" --build-arg "VERSION=$version" \
        --tag "openmusic-meting-api-go:$version" \
        --output "type=docker,dest=$build_dir/$arch.tar" "$project_dir"
    gzip -n -c "$build_dir/$arch.tar" > "$output_dir/$archive"
    assets+=("$archive")
done

cd "$output_dir"
if command -v sha256sum >/dev/null; then
    sha256sum "${assets[@]}" > SHA256SUMS.docker
else
    shasum -a 256 "${assets[@]}" > SHA256SUMS.docker
fi
