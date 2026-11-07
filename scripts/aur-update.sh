#!/usr/bin/env bash
# Regenerate the AUR package metadata after a release.
#
#   ./scripts/aur-update.sh [version]
#
# Expects dist/zdl_<version>_linux_{amd64,arm64}.tar.gz to exist (run
# `make release` first), then refreshes pkgver, checksums, and .SRCINFO.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(sed -n 's/^VERSION ?= //p' Makefile)}"
PKGDIR="packaging/aur/zdl-bin"
AMD64="dist/zdl_${VERSION}_linux_amd64.tar.gz"
ARM64="dist/zdl_${VERSION}_linux_arm64.tar.gz"

for f in "$AMD64" "$ARM64"; do
	if [[ ! -f "$f" ]]; then
		echo "aur-update: missing $f (run make release VERSION=$VERSION)" >&2
		exit 1
	fi
done

SUM_AMD64=$(sha256sum "$AMD64" | cut -d' ' -f1)
SUM_ARM64=$(sha256sum "$ARM64" | cut -d' ' -f1)

VERSION="$VERSION" SUM_AMD64="$SUM_AMD64" SUM_ARM64="$SUM_ARM64" PKGDIR="$PKGDIR" \
	python3 - <<'PY'
import os
import pathlib
import re

path = pathlib.Path(os.environ["PKGDIR"]) / "PKGBUILD"
text = path.read_text()
version = os.environ["VERSION"]
subs = {
    r"^pkgver=.*$": f"pkgver={version}",
    r"^pkgrel=.*$": "pkgrel=1",
    r"^sha256sums_x86_64=.*$": f"sha256sums_x86_64=('{os.environ['SUM_AMD64']}')",
    r"^sha256sums_aarch64=.*$": f"sha256sums_aarch64=('{os.environ['SUM_ARM64']}')",
}
for pattern, repl in subs.items():
    text, n = re.subn(pattern, repl, text, flags=re.M)
    if n != 1:
        raise SystemExit(f"aur-update: {pattern} matched {n} times")
path.write_text(text)
print(f"updated {path} to {version}")
PY

(
	cd "$PKGDIR"
	makepkg --printsrcinfo >.SRCINFO
)
echo "regenerated $PKGDIR/.SRCINFO"
echo
echo "Next: build with 'cd $PKGDIR && makepkg -f', then publish in your AUR checkout:"
echo "  cp $PKGDIR/{PKGBUILD,.SRCINFO} . && git commit -am 'zdl-bin $VERSION-1' && git push"
