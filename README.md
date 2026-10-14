# cashsdk-cli

The command-line interface for [CashSDK](https://cashsdk.com): in-app purchases,
subscriptions and paywalls for iOS, macOS, Android, web, Chrome extensions and standalone Stripe.

> Version 2.1.0 adds browser login and all seven platform choices.
> Merchant checkout and native store purchases have their own integration acceptance gates.

One binary, no runtime dependencies. It performs every deterministic setup step
against your CashSDK account, shows you exactly what is left, and generates the
typed constants and integration snippets your app code needs.

## Install

Homebrew (macOS and Linux):

```bash
brew install cashsdk/tap/cashsdk
```

Or download the binary for your platform from the
[latest release](https://github.com/CashSDK/cashsdk-cli/releases/latest) and put it on
your `PATH`. Builds are published for macOS (Apple silicon and Intel), Linux (x64 and
arm64) and Windows (x64). `cashsdk version` prints what you installed.

npm (macOS, Linux and Windows):

```bash
npm install -g cashsdk-cli@2.1.0
# Or run without a global install:
npx cashsdk-cli@2.1.0 version
```

Version 2.1.0 is published with build provenance and the same five platform binaries as
the GitHub release. `cashsdk version` should print `cashsdk 2.1.0`.

## Sign in

Sign in through your browser:

```bash
cashsdk login
```

Match the terminal verification code, select your workspace and optional app, and allow access.
The browser reports success after credentials are saved privately. `login --no-browser` prints
the link to open on the same computer. Access tokens refresh automatically.

For CI or an existing setup token, use `cashsdk login --with-token` or `auth set` with a
token from your [dashboard](https://app.cashsdk.com):

| Token | Where to get it | Scope |
| --- | --- | --- |
| Setup token (`csk_st_`) | your app's page, "Generate prompt" | one app, setup routes only, expires after 24h |
| MCP token (`csk_mcp_`) | Settings, MCP, "Create token" | your workspace, durable; use this for daily work and CI |

```bash
cashsdk auth set --app app_your_id --token csk_st_...
```

The token is stored once at `~/.config/cashsdk/config.json` (mode 0600), so it
never has to appear on a command line again. `CASHSDK_TOKEN` and `CASHSDK_APP`
environment variables override the file, which is the way to authenticate in CI.

Note: secret keys (`csk_sk_`) authenticate the server data API, not the CLI.
The CLI refuses them with an explanation instead of failing with a bare 401.

## From zero to a working setup

```bash
cashsdk doctor          # reachability, credential, app, checklist in one pass
cashsdk setup guide     # what is done, what is next, the command for each step
cashsdk setup run       # every deterministic step, in one idempotent pass
cashsdk snippets        # compilable SDK snippets to apply in your app code
cashsdk setup verify --wait   # watches until every checklist item passes
```

`setup run` syncs your store catalog, wires a paywall, placement and campaign,
prints the store-console steps only you can do, and requests the notification
test. Re-running is always safe: finished steps report `unchanged`. Two things
are never guessed: which entitlement a product unlocks (pass
`--map product=entitlement` or `--map-all pro`), and anything requiring your
store credentials, which upload only in the dashboard, never through the CLI
or an agent.

For web, direct Mac, Chrome extension and standalone Stripe apps, use Platforms and Stripe
in the dashboard or the remote MCP commerce tools. Native store synchronization does not apply.
`cashsdk snippets --platform` accepts `ios`, `macos`, `android`, `web`, `macos-direct`,
`chrome-extension` and `stripe`.

`--map pro=access` covers every catalog row for `pro`, including each Play base
plan. Use `--map pro:annual=premium` for one base plan; that specific selection
wins over a broad product selection. Existing links are preserved, and an
already-present mapping is not rewritten. `--map-all` fills only unmapped active
access rows, excluding consumables, inactive rows and products explicitly sold
without access. `cashsdk catalog` shows `identifier:basePlanId` so the rows are
distinguishable.

Exit codes tell you what happened: `0` done, `2` usage, `3` auth, `4` remote
error, `5` blocked on a step only a human can do, `6` finished with items that
need attention.

## Working with a coding agent

Every command takes `--json` and prints exactly one machine-readable document
on stdout. Errors go to stderr. The one-paste onboarding prompt from your
dashboard drives this CLI; you can also connect your agent to the hosted MCP
server:

```bash
cashsdk connect claude   # prints the `claude mcp add` one-liner
cashsdk connect cursor --write   # writes .cursor/mcp.json
cashsdk connect codex    # prints the ~/.codex/config.toml block
```

Each client uses its own browser OAuth authorization. The CLI does not copy its access token
into another client's configuration. `--with-token` explicitly selects a durable MCP-token
configuration. The hosted bundle and skill install guide are at
[Agent plugin](https://docs.cashsdk.com/ai-tools/agent-plugin).

## Commands

| Command | What it does |
| --- | --- |
| `login`, `auth set/status/clear` | store, inspect or remove credentials |
| `whoami` | which credential this invocation resolves to, verified live |
| `doctor` | reachability, credential, app and checklist in one pass |
| `setup guide` | what is next, with the exact command for each step |
| `setup run` | all deterministic setup in one idempotent pass |
| `setup status` | the live checklist, computed from observed state |
| `setup verify [--wait]` | watch until every item passes |
| `snippets` | integration snippets for all seven platform choices |
| `catalog` / `catalog sync` / `catalog push` | inspect and sync products with the store |
| `codegen` | typed catalog constants (`--lang swift\|kotlin\|typescript`) |
| `paywalls`, `templates` | list paywalls and templates |
| `apps` | list workspace apps (MCP token) or show the configured app |
| `events`, `transactions` | tail what the platform observes |
| `connect` | MCP config for Claude Code, Cursor or Codex |

Run `cashsdk <command> --help` for flags and examples.

## Links

- Documentation: <https://docs.cashsdk.com/cli/overview>
- Dashboard: <https://app.cashsdk.com>
- Machine-readable docs: <https://api.cashsdk.com/llms.txt>

## License

Commercial. See [LICENSE.md](./LICENSE.md). Production use requires a CashSDK
subscription. Notices for statically linked open-source dependencies are in
[THIRD_PARTY_NOTICES.md](./THIRD_PARTY_NOTICES.md).
