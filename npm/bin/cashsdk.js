#!/usr/bin/env node
// Launcher for the cashsdk CLI. The npm package embeds a native binary per
// platform; this picks the right one and executes it with the caller's argv,
// stdio and exit code passed straight through.
"use strict";
const { spawnSync } = require("node:child_process");
const { existsSync } = require("node:fs");
const path = require("node:path");

const platforms = {
  "darwin arm64": "darwin_arm64/cashsdk",
  "darwin x64": "darwin_amd64/cashsdk",
  "linux x64": "linux_amd64/cashsdk",
  "linux arm64": "linux_arm64/cashsdk",
  "win32 x64": "windows_amd64/cashsdk.exe",
};

const key = `${process.platform} ${process.arch}`;
const rel = platforms[key];
if (!rel) {
  console.error(
    `cashsdk: no prebuilt binary for ${key}.\n` +
      `Supported: ${Object.keys(platforms).join(", ")}.\n` +
      `Install with Homebrew instead (brew install cashsdk/tap/cashsdk) or contact support.`,
  );
  process.exit(1);
}

const bin = path.join(__dirname, "..", "dist", rel);
if (!existsSync(bin)) {
  console.error(`cashsdk: packaged binary missing at ${bin}; reinstall the package.`);
  process.exit(1);
}

const res = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (res.error) {
  console.error(`cashsdk: failed to start: ${res.error.message}`);
  process.exit(1);
}
process.exit(res.status === null ? 1 : res.status);
