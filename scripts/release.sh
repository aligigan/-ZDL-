#!/usr/bin/env bash
# Build portable, statically linked zdl archives for Linux.
#
#   ./scripts/release.sh [version]
#
# Output: dist/zdl_<version>_linux_<arch>.tar.gz
#         dist/SHA256SUMS
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-0.2.0}"
OUT="dist"
NAME="zdl"

targets=(
	linux/amd64
	linux/arm64
)

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$@"
	else
		echo "release: need sha256sum or shasum" >&2
		exit 1
	fi
}

rm -rf "$OUT"
mkdir -p "$OUT"

for target in "${targets[@]}"; do
	os="${target%/*}"
	arch="${target#*/}"
	stage="$OUT/${NAME}_${VERSION}_${os}_${arch}"

	echo "building $os/$arch"
	mkdir -p "$stage"
	GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
		go build -trimpath -buildvcs=false \
		-ldflags "-s -w -X main.version=$VERSION" \
		-o "$stage/$NAME" ./cmd/zdl
	cp README.md "$stage/"
	cp CHANGELOG.md "$stage/"
	cp LICENSE "$stage/"
	cp -r completions "$stage/"

	tar -C "$OUT" -czf "$OUT/${stage##*/}.tar.gz" "${stage##*/}"
	rm -rf "$stage"
done

(
	cd "$OUT"
	sha256 zdl_* > SHA256SUMS
)

echo
echo "artifacts in $OUT:"
ls -lh "$OUT"
