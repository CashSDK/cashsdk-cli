# AGENTS.md: cashsdk CLI

Instructions for coding agents working on the `cashsdk` command-line tool.
The public repository github.com/CashSDK/cashsdk-cli is published from the CashSDK source tree, so direct edits there are overwritten by the next release.

## Purpose

- `cashsdk` is one static Go binary (module `github.com/cashsdk/cashsdk-cli`). It runs the deterministic setup steps against a CashSDK account and prints typed catalog constants and SDK snippets.
- One build ships three ways: GitHub releases on CashSDK/cashsdk-cli, the Homebrew formula `cashsdk/tap/cashsdk` (repo CashSDK/homebrew-tap), and the npm package `cashsdk-cli`. Never publish under an `@cashsdk/` scope.
- The license is commercial (`LICENSE.md`). `THIRD_PARTY_NOTICES.md` covers the statically linked dependencies.

## Build and test

```bash
go build ./...
go build -o dist/cashsdk .        # local binary; `dist/cashsdk version` prints `cashsdk dev`
go vet ./...
go test -race ./...               # unit tests plus the mock API contract suite
go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 -quiet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
scripts/check-third-party-notices.sh
```

- CI in the CashSDK source tree (job `cli-go`) runs every command above except the `-o` build. `publish.yml` runs vet, the race tests, gosec, govulncheck and the notice check before it builds.
- govulncheck gates every release: 2.0.1 shipped a Go standard library with reachable vulnerabilities.
- `go.mod` pins `go 1.25.13`, and CI and `publish.yml` take the Go version from it. Build releases with Go 1.25.13 or newer.
- There is no Makefile. The `package.json` scripts `build`, `dev`, `test` and `vet` wrap the go commands; `clean` deletes `dist/`.
- Live smoke against production: `go build -o dist/cashsdk . && go run ./scripts/livesmoke`. It signs in once as a throwaway `smoke-` user, creates a `Smoke cli` workspace, runs the built binary through the command tour and schedules the workspace's deletion. Env: `BASE` (default `https://api.cashsdk.com`), `CLI` (default `dist/cashsdk`), `SMOKE_CLEANUP=0` keeps the workspace.
- npm packaging: `npm/package.json` is a template (`"version": "__VERSION__"`) and `npm/bin/cashsdk.js` launches the embedded binary for darwin arm64/x64, linux x64/arm64 or win32 x64. `scripts/release.sh <version>` stages the package in `dist/release/npm`; inspect it with `(cd dist/release/npm && npm pack --dry-run)`.

## Map

- `main.go`: entry point. Holds `version`, which release builds stamp with `-ldflags "-X main.version=<v>"`, and calls `cmd.Dispatch`.
- `internal/cmd`: argv router, global flags and help sections (`root.go`), flag parser (`flags.go`) and one file per command group. `cmd_test.go` is the mock API contract suite; `regressions_test.go` pins the 2026-08-27 audit fixes.
- `internal/api`: the HTTP client behind every command: bearer token, `User-Agent: cashsdk-cli/<version>`, 60 s timeout, API errors mapped to exit codes.
- `internal/config`: the credential file, token kinds, masking and flag/env/file resolution. `private_file.go` and `replace_*.go` write it atomically with mode 0600.
- `internal/codegen`: typed catalog constants for Swift, Kotlin and TypeScript.
- `internal/ui`: terminal output: color and glyph rules, the stderr spinner, `--json` mode, `ExitError`.
- `npm/`: the npm package template and its Node launcher.
- `scripts/`: `release.sh`, `make-formula.sh`, `check-third-party-notices.sh`, `npm-enable-trusted-publishing.sh` and the `livesmoke/` program.
- `.github/workflows/publish.yml`: the manual npm publish workflow. It only runs in the public repository, where this directory is the root.

## Release

The full runbook is `docs/cli/07-DISTRIBUTION.md` in the CashSDK source tree. In order:

1. Version: no file holds it. Pick `X.Y.Z` (tag `vX.Y.Z`); `release.sh` stamps it into the binaries and the staged `npm/package.json`. Bump any CLI version a doc or onboarding prompt pins in the same change.
2. Gates: vet, `go test -race`, govulncheck, then the live smoke.
3. Binaries: `scripts/release.sh "$VERSION"` writes darwin arm64/amd64 and linux amd64/arm64 `.tar.gz` archives, a windows amd64 `.zip`, `checksums.txt` and the npm package to `dist/release/`.
4. GitHub: copy only the files `git ls-files .` lists into a fresh repo, commit them as one root commit, force-push `main` of CashSDK/cashsdk-cli, then `gh release create "v$VERSION"` with the archives and `checksums.txt`.
5. Homebrew: `scripts/make-formula.sh "$VERSION" dist/release/checksums.txt` renders `Formula/cashsdk.rb`; commit and push it in CashSDK/homebrew-tap.
6. npm: `gh workflow run publish.yml --ref "v$VERSION" -f version="$VERSION" --repo CashSDK/cashsdk-cli` (trusted publishing; dispatch on the tag, because the workflow refuses to run unless the tag points at the commit it runs on, and `main` moves on every re-export), or an interactive `npm publish` from `dist/release/npm` with a 2FA code.
7. Verify by behavior: `brew update && brew upgrade cashsdk || brew install cashsdk/tap/cashsdk`, then `cashsdk version`; `npx -y cashsdk-cli@$VERSION version` from a clean directory.

Current state:

- 2.0.2 is released on GitHub and Homebrew.
- npm still serves the broken 1.0.1. Publishing 2.0.2 needs the owner: GitHub Actions for the CashSDK organisation was stopped by a billing problem (2026-09-26), and the npm trusted publisher (or an npm login with 2FA) must be in place.
- Do not publish 2.0.1. Its binaries used a Go standard library with reachable vulnerabilities; its GitHub release and tag are deleted.
- Tag `v2.0.2` points at an earlier export than `main` (only `README.md` differs), so dispatch `publish.yml` with `--ref v2.0.2`.

## Rules that bite

- Run the export's `git init` and `git add` with `-c core.excludesFile=/dev/null`. A global gitignore containing `*.mod` once dropped `go.mod` from the public copy (2026-08-27) and broke the publish workflow.
- `release.sh` refuses a dirty tree (untracked files count) and any version that is not plain `X.Y.Z`.
- `publish.yml` refuses to publish unless tag `v<version>` points at the commit it runs on and each rebuilt binary matches the GitHub release asset byte for byte.
- Formula checksums come from `checksums.txt` through `make-formula.sh`, never typed by hand.
- npm: never create a token that bypasses 2FA and never keep a long-lived publish token on a machine. `scripts/npm-enable-trusted-publishing.sh` is the one-time trusted-publisher setup and needs account 2FA plus `npm login`.
- Verify before publishing: npm versions are immutable, and npx caches make a bad publish sticky.
- A new Go dependency needs an entry in `THIRD_PARTY_NOTICES.md`, or `check-third-party-notices.sh` fails CI and `release.sh`.
- The license is commercial: `npm/package.json` points at `LICENSE.md`, and the README says so. The 1.0.1 package had a contradictory MIT footer; do not bring it back.
- `cashsdk version` on a binary not built by `release.sh` prints `cashsdk dev`. That is deliberate.
- Every visible command must be listed in `helpSections` in `root.go`, and help and codegen output must not contain an em dash. Tests enforce both.
- `--json` prints exactly one JSON document on stdout; errors go to stderr. Exit codes: 0 ok, 1 gate failed (`--require-complete`), 2 usage, 3 auth, 4 remote error, 5 blocked on a human step, 6 finished with items needing attention.
- Credentials resolve in this order: flags, then `CASHSDK_TOKEN`/`CASHSDK_APP`/`CASHSDK_API_URL`, then `$XDG_CONFIG_HOME/cashsdk/config.json` (default `~/.config/cashsdk/config.json`, 0600 in a 0700 directory, symlinks refused). Secret keys (`csk_sk_`) are refused up front.
- The API client refuses redirects, so a token never reaches another host, and allows plain HTTP only for localhost.
- Never guess: product-to-entitlement mapping is written only through `--map`/`--map-all`, and store credentials never pass through the CLI (`setup run` stops with exit 5).
