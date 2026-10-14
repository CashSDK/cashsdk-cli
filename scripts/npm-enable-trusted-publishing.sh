#!/usr/bin/env bash
# One-time setup so cashsdk-cli publishes from CI with no token and no OTP.
#
#   scripts/npm-enable-trusted-publishing.sh
#
# Prerequisites, both on the npm account that owns cashsdk-cli:
#   1. Two-factor authentication ENABLED (npmjs.com, Account, Two-Factor
#      Authentication). This is not optional: npm requires account-level 2FA
#      both to publish and to run `npm trust`, and an account without it hits
#      an EOTP prompt that nothing can answer.
#   2. A current login: `npm login`.
#
# After this runs once, every release publishes with:
#   gh workflow run publish.yml -f version=<v> --repo CashSDK/cashsdk-cli
set -euo pipefail

PKG=cashsdk-cli
REPO=CashSDK/cashsdk-cli
WORKFLOW=publish.yml

# npm trust needs npm 11.15+. Pin a version that supports the repository's
# Node floor as well as this machine's Node, without changing global tools.
NPM=(npx --yes npm@11.15.0)

echo "==> npm identity"
"${NPM[@]}" whoami || {
  echo "not logged in. Run: npm login" >&2
  exit 1
}

echo "==> current trust relationships for $PKG"
"${NPM[@]}" trust list "$PKG" || true

echo "==> granting $REPO/$WORKFLOW permission to publish $PKG"
"${NPM[@]}" trust github "$PKG" \
  --file "$WORKFLOW" \
  --repo "$REPO" \
  --allow-publish

echo "==> verifying"
"${NPM[@]}" trust list "$PKG"

cat <<EOF

Trusted publishing is configured. Release from now on with:

  gh workflow run publish.yml -f version=<version> --repo $REPO
  gh run watch --repo $REPO --exit-status

No npm token is stored anywhere, and provenance is attached automatically.
EOF
