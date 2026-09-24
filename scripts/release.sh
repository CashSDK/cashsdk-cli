#!/usr/bin/env bash
# Build every release artifact for the cashsdk CLI.
#
#   scripts/release.sh 2.0.2
#
# Produces, under dist/release/:
#   cashsdk_<v>_darwin_arm64.tar.gz     (also darwin_amd64, linux_amd64, linux_arm64)
#   cashsdk_<v>_windows_amd64.zip
#   checksums.txt                        sha256 of every archive
#   npm/                                 ready-to-publish package "cashsdk-cli"
#
# The same binaries feed the GitHub release (Homebrew installs from it) and the
# npm package (which embeds all platforms and picks one at runtime).
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: scripts/release.sh <version>}"
OUT="dist/release"
LDFLAGS="-s -w -X main.version=${VERSION}"

if [[ ! "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "invalid release version: $VERSION" >&2
  exit 2
fi

if [[ -n "$(git status --porcelain --untracked-files=all -- .)" ]]; then
  echo "refusing to release from a dirty CLI source tree" >&2
  git status --short -- . >&2
  exit 1
fi

scripts/check-third-party-notices.sh

rm -rf "$OUT"
mkdir -p "$OUT/npm/bin"

targets=(
  "darwin arm64"
  "darwin amd64"
  "linux amd64"
  "linux arm64"
  "windows amd64"
)

for t in "${targets[@]}"; do
  read -r GOOS GOARCH <<<"$t"
  bin="cashsdk"
  [ "$GOOS" = "windows" ] && bin="cashsdk.exe"
  stage="$OUT/stage/${GOOS}_${GOARCH}"
  mkdir -p "$stage"
  echo "building ${GOOS}/${GOARCH}"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -buildvcs=false -trimpath -ldflags "$LDFLAGS" -o "$stage/$bin" .
  cp LICENSE.md README.md THIRD_PARTY_NOTICES.md "$stage/"

  # npm embeds the raw binaries; archives feed the GitHub release.
  mkdir -p "$OUT/npm/dist/${GOOS}_${GOARCH}"
  cp "$stage/$bin" "$OUT/npm/dist/${GOOS}_${GOARCH}/$bin"

  if [ "$GOOS" = "windows" ]; then
    (cd "$stage" && zip -q -X "../../cashsdk_${VERSION}_${GOOS}_${GOARCH}.zip" "$bin" LICENSE.md README.md THIRD_PARTY_NOTICES.md)
  else
    tar -czf "$OUT/cashsdk_${VERSION}_${GOOS}_${GOARCH}.tar.gz" -C "$stage" "$bin" LICENSE.md README.md THIRD_PARTY_NOTICES.md
  fi
done
rm -rf "$OUT/stage"

(cd "$OUT" && shasum -a 256 cashsdk_"${VERSION}"_* > checksums.txt)

# Assemble the npm package.
cp npm/bin/cashsdk.js "$OUT/npm/bin/cashsdk.js"
cp LICENSE.md README.md THIRD_PARTY_NOTICES.md "$OUT/npm/"
sed "s/__VERSION__/${VERSION}/" npm/package.json > "$OUT/npm/package.json"

echo
echo "artifacts in $OUT:"
ls -la "$OUT"
echo
echo "next steps:"
echo "  npm:   (cd $OUT/npm && npm publish)"
echo "  github: gh release create v${VERSION} $OUT/cashsdk_${VERSION}_* --repo CashSDK/cashsdk-cli --title \"cashsdk ${VERSION}\""
echo "  brew:  update Formula/cashsdk.rb in CashSDK/homebrew-tap with the new version + sha256s from checksums.txt"
