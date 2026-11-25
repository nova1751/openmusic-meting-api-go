#!/usr/bin/env bash
set -eo pipefail

base="${1:-}"
head="${2:-HEAD}"
if [[ -z "$base" || "$base" =~ ^0+$ ]] || ! git cat-file -e "$base^{commit}" 2>/dev/null; then
    base="$(git describe --tags --abbrev=0 "$head^" 2>/dev/null || true)"
fi
if [[ -z "$base" ]] || ! git cat-file -e "$base^{commit}" 2>/dev/null; then
    base="$(git rev-parse "$head^" 2>/dev/null || true)"
fi
if [[ -z "$base" ]]; then
    printf 'No comparison commit available; incremental checks require a baseline.\n' >&2
    exit 1
fi

files=()
while IFS= read -r -d '' file; do
    [[ -f "$file" ]] && files+=("$file")
done < <(git diff --name-only -z --diff-filter=ACMR "$base" "$head" -- '*.go')
if ((${#files[@]} == 0)); then
    printf 'No changed Go files.\n'
    exit 0
fi
unformatted="$(gofmt -l "${files[@]}")"
if [[ -n "$unformatted" ]]; then
    printf 'Unformatted Go files:\n%s\n' "$unformatted" >&2
    exit 1
fi

packages=()
for file in "${files[@]}"; do
    package="./$(dirname -- "$file")"
    found=false
    for item in "${packages[@]}"; do
        if [[ "$item" == "$package" ]]; then
            found=true
            break
        fi
    done
    if [[ "$found" == false ]]; then
        packages+=("$package")
    fi
done
printf 'Checking changed Go packages from %s to %s.\n' "$base" "$head"
go vet "${packages[@]}"
