#!/usr/bin/env bash
# Render the Homebrew formula for a released version.
#
#   scripts/make-formula.sh 2.0.2 dist/release/checksums.txt > cashsdk.rb
#
# Paste the output into Formula/cashsdk.rb in the CashSDK/homebrew-tap repo.
set -euo pipefail

VERSION="${1:?usage: scripts/make-formula.sh <version> <checksums.txt>}"
SUMS="${2:?usage: scripts/make-formula.sh <version> <checksums.txt>}"

if [[ ! "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "invalid release version: $VERSION" >&2
  exit 2
fi

sha() { awk -v f="cashsdk_${VERSION}_$1" '$2 == f { print $1 }' "$SUMS"; }

DARWIN_ARM=$(sha darwin_arm64.tar.gz)
DARWIN_AMD=$(sha darwin_amd64.tar.gz)
LINUX_AMD=$(sha linux_amd64.tar.gz)
LINUX_ARM=$(sha linux_arm64.tar.gz)

for v in DARWIN_ARM DARWIN_AMD LINUX_AMD LINUX_ARM; do
  [[ "${!v}" =~ ^[0-9a-f]{64}$ ]] || { echo "missing or invalid checksum for $v" >&2; exit 1; }
done

BASE="https://github.com/CashSDK/cashsdk-cli/releases/download/v${VERSION}"

cat <<EOF
class Cashsdk < Formula
  desc "Manage in-app purchases, subscriptions and paywalls from your terminal"
  homepage "https://docs.cashsdk.com/cli/overview"
  license :cannot_represent # commercial, see LICENSE.md in the archive

  on_macos do
    if Hardware::CPU.arm?
      url "${BASE}/cashsdk_${VERSION}_darwin_arm64.tar.gz"
      sha256 "${DARWIN_ARM}"
    else
      url "${BASE}/cashsdk_${VERSION}_darwin_amd64.tar.gz"
      sha256 "${DARWIN_AMD}"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "${BASE}/cashsdk_${VERSION}_linux_arm64.tar.gz"
      sha256 "${LINUX_ARM}"
    else
      url "${BASE}/cashsdk_${VERSION}_linux_amd64.tar.gz"
      sha256 "${LINUX_AMD}"
    end
  end

  def install
    bin.install "cashsdk"
  end

  test do
    assert_match "cashsdk #{version}", shell_output("#{bin}/cashsdk version")
  end
end
EOF
