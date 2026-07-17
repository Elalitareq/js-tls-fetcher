// postinstall: ensure the Go helper binary exists for this platform.
//
// Strategy:
//   1. If node/bin/tlsfetch-server(.exe) already exists, do nothing.
//   2. If the Go source (go.mod up the tree) and the `go` toolchain are present,
//      build it (works when installed from the cloned repo).
//   3. Otherwise download the prebuilt binary for this OS/arch from the repo's
//      GitHub Releases (works for a plain `npm install`).
//
// Never hard-fails the install: on failure it prints guidance and exits 0, so
// the user can still run `npm run build` manually.

import { existsSync, mkdirSync, chmodSync, createWriteStream, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";
import https from "node:https";

const __dirname = dirname(fileURLToPath(import.meta.url));
const pkgDir = resolve(__dirname, ".."); // the node/ package dir
const binDir = join(pkgDir, "bin");
const isWin = process.platform === "win32";
const exeName = isWin ? "tlsfetch-server.exe" : "tlsfetch-server";
const dest = join(binDir, exeName);

const GOOS = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
const GOARCH = { x64: "amd64", arm64: "arm64" }[process.arch];

function log(msg) {
  console.log(`[tls-fetcher] ${msg}`);
}

function findModuleRoot(start) {
  let dir = start;
  for (let i = 0; i < 6; i++) {
    if (existsSync(join(dir, "go.mod")) && existsSync(join(dir, "cmd", "server"))) return dir;
    const up = dirname(dir);
    if (up === dir) break;
    dir = up;
  }
  return null;
}

function tryBuild() {
  const root = findModuleRoot(pkgDir);
  if (!root) return false;
  try {
    execFileSync("go", ["version"], { stdio: "ignore" });
  } catch {
    return false;
  }
  log(`building helper with Go from ${root} ...`);
  execFileSync("go", ["build", "-trimpath", "-ldflags", "-s -w", "-o", dest, "./cmd/server"], {
    cwd: root,
    stdio: "inherit",
    env: { ...process.env, CGO_ENABLED: "0" },
  });
  return existsSync(dest);
}

function repoSlug() {
  try {
    const pkg = JSON.parse(readFileSync(join(pkgDir, "package.json"), "utf8"));
    const url = (pkg.repository && (pkg.repository.url || pkg.repository)) || "";
    const m = String(url).match(/github\.com[/:]([^/]+\/[^/.]+)/);
    if (m) return m[1];
  } catch {}
  return null;
}

function download(url, out, redirects = 0) {
  return new Promise((res, rej) => {
    if (redirects > 5) return rej(new Error("too many redirects"));
    https
      .get(url, { headers: { "User-Agent": "tls-fetcher-postinstall" } }, (r) => {
        if (r.statusCode >= 300 && r.statusCode < 400 && r.headers.location) {
          r.resume();
          return download(r.headers.location, out, redirects + 1).then(res, rej);
        }
        if (r.statusCode !== 200) {
          r.resume();
          return rej(new Error(`HTTP ${r.statusCode} for ${url}`));
        }
        const f = createWriteStream(out);
        r.pipe(f);
        f.on("finish", () => f.close(() => res()));
        f.on("error", rej);
      })
      .on("error", rej);
  });
}

async function tryDownload() {
  const slug = repoSlug();
  if (!slug || !GOOS || !GOARCH) return false;
  const asset = `tlsfetch-server-${GOOS}-${GOARCH}${isWin ? ".exe" : ""}`;
  const url = `https://github.com/${slug}/releases/latest/download/${asset}`;
  log(`downloading prebuilt helper: ${url}`);
  await download(url, dest);
  return existsSync(dest);
}

async function main() {
  if (existsSync(dest)) {
    log("helper already present, skipping.");
    return;
  }
  mkdirSync(binDir, { recursive: true });

  try {
    if (tryBuild()) {
      if (!isWin) chmodSync(dest, 0o755);
      log("built helper from source ✓");
      return;
    }
  } catch (e) {
    log(`go build failed: ${e.message}`);
  }

  try {
    if (await tryDownload()) {
      if (!isWin) chmodSync(dest, 0o755);
      log("downloaded prebuilt helper ✓");
      return;
    }
  } catch (e) {
    log(`download failed: ${e.message}`);
  }

  log(
    "could not obtain the Go helper automatically. Install Go and run " +
      "`npm run build`, or download the binary for your platform from the repo's Releases " +
      "into node/bin/" + exeName + "."
  );
}

main().catch((e) => {
  log("postinstall error: " + e.message);
  // do not fail the whole npm install
});
