# CLAUDE.md

Context for Claude Code working in this repo. Read this first.

## What this project is

**tls-fetcher** is a request handler that performs HTTPS requests using a
**caller-chosen TLS fingerprint** — either a named browser profile (Chrome,
Firefox, Safari/iOS) or a **raw JA3 string** — through an **optional proxy**
(http/https/socks5, with auth). Each request reproduces a real browser's TLS
fingerprint (JA3/JA4) *and* HTTP/2 fingerprint ("Akamai" fingerprint), so it is
useful for talking to services that fingerprint clients (anti-bot, scraping,
testing your own fingerprint defenses).

It is exposed three ways:

1. **Go library** (`fetcher/`) — `fetcher.New(...).Do(Job{...})`.
2. **Go CLI** (`cmd/fetch/`) — rotates a set of fingerprints and proxies over a
   target URL.
3. **Node package** (`node/`) — a JS API that runs requests **through a Go
   helper process** (`cmd/server`). This is the working way to use it from Node
   (see "Why not WASM" below).

## Repository layout

```
package.json           npm package `js-tls-fetcher` — MUST stay at the repo root
fetcher/fetcher.go     Go library: Handler, Job, Fingerprint, client cache,
                       named profiles, custom-JA3 -> ClientProfile builder
cmd/fetch/main.go      CLI: rotate fingerprints (JSONL) + proxies over a URL
cmd/server/main.go     NDJSON-over-stdio helper the Node package spawns
node/index.js          Node API (TlsClient): spawns helper, correlates by id
node/index.d.ts        TypeScript types for the Node API
node/scripts/postinstall.js  builds helper with Go, else downloads from Releases
node/test.js           end-to-end test (Node -> Go -> local echo server)
.github/workflows/     ci.yml (build+test+cross-compile+pack-check),
                       release.yml (binaries -> Release, then npm publish)
```

Single Go module (`go.mod`, module path `tls-fetcher`). Go 1.24.

The Go module is still named `tls-fetcher`; only the **npm package** and the
GitHub repo are `js-tls-fetcher`. Don't "fix" one to match the other.

**`package.json` lives at the repo root on purpose.** npm cannot install from a
git subdirectory, so moving it back into `node/` would break
`npm install github:Elalitareq/js-tls-fetcher`. The binary still lives at
`node/bin/`, resolved relative to `node/index.js`. The `files` array ships the
Go source (`cmd/`, `fetcher/`, `go.mod`, `go.sum`) so postinstall can build on
a machine that has Go — `pack-check` in CI fails the build if that stops being
true.

## Common commands

```bash
# Go
go build ./...           # build everything
go vet ./...
go run ./cmd/fetch -url https://tls.peet.ws/api/all -profile chrome_146

# Node (from the repo ROOT, not node/)
npm run build            # build the Go helper into node/bin (needs Go)
npm test                 # end-to-end test, expect "ALL PASS" (8 checks)
npm pack --ignore-scripts && tar -tzf js-tls-fetcher-*.tgz   # inspect what ships

# Build the helper for a specific platform (pure Go, no CGO):
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o node/bin/tlsfetch-server ./cmd/server
```

## Key facts and gotchas

- **Do NOT add `github.com/refraction-networking/utls` to this module.** This
  module depends on `github.com/bogdanfinn/tls-client`, which uses its own uTLS
  fork (`bogdanfinn/utls`). Having both forks in one module can bump shared deps
  (x/crypto, x/net) and make tls-client's fork **silently fall back to a plain
  TLS 1.2 handshake** (the JA3 becomes Go's stdlib default, not a browser).
  Keep any refraction-utls-based tooling in a separate module.

- **Verifying the fingerprint locally, not over the internet.** Some sandboxed
  environments route outbound HTTPS through a TLS-terminating proxy, so an
  external checker (tls.peet.ws) sees the *proxy's* handshake, not ours — every
  profile then looks identical. To verify what we actually emit, capture the
  ClientHello on a **localhost** TCP listener and parse its JA3 (this is how the
  hello was validated during development). On a normal machine (no MITM proxy),
  external checkers show the real fingerprint.

- **Pure Go / cross-compile.** `cmd/server` has no CGO, so it cross-compiles to
  darwin/arm64, darwin/amd64, linux, windows. The release workflow builds all of
  them on a `v*` tag and attaches them to the GitHub Release; postinstall
  downloads the right one when Go isn't available.

- **Named profiles** live in `namedProfiles` in `fetcher/fetcher.go`:
  `chrome_120/131/133/144/146`, `firefox_132/135/148`, `safari_16`,
  `safari_ios_17_0/18_0/18_5/26_0`. Bump these when tls-client adds newer ones.

- **Custom JA3** (`customProfileFromJA3`) fills the parts JA3 can't encode
  (signature algorithms, TLS versions, key-share curves, ALPN/ALPS, ECH, cert
  compression, and the HTTP/2 settings) with **modern-Chrome-like defaults**. If
  you feed non-Chrome JA3s and need exact parity, adjust those defaults.

## Node binding protocol

`cmd/server` reads one JSON request per line on stdin and writes one JSON
response per line on stdout; requests are correlated by `id` and run
concurrently. Response bodies are base64 in `body_b64`. `node/index.js` wraps
this with a Promise-based `TlsClient.request()`.

## Why not WASM (came up during design)

TLS fingerprinting needs a **raw TCP socket** to write a custom ClientHello. A
WASM sandbox (`js/wasm` and Node's WASI) has no raw sockets; network calls go
through the host's `fetch`, which uses Node/V8's TLS and fixes the JA3 to Node's
fingerprint. So Go->WASM can't impersonate. The native-helper-over-stdio design
(same approach as CycleTLS) is the working alternative.

## Status / possible next steps

- Core library, CLI, Node package, CI, release workflow, postinstall: done and
  tested (`npm test` passes 8 checks).
- Installable via `npm install github:Elalitareq/js-tls-fetcher` today.
- npm registry publish is wired but **inert until an `NPM_TOKEN` repo secret
  exists** — `publish-npm` warns and skips without it, by design, so the GitHub
  install path never depends on registry auth.
- Releasing: bump `package.json` version, then push a matching `v*` tag.
  `verify-version` hard-fails on a tag/version mismatch. `publish-npm` runs
  `needs: build` so Release assets always exist before the package that
  downloads them.
- Not yet done: request retries/backoff, response streaming, per-request cookie
  jars, and TLS/HTTP2 fingerprint assertions in CI (would need a non-proxied
  runner or the localhost-capture technique).
- Companion (separate module, not in this repo): JA3 *generators* built on
  refraction-networking/utls that emit large sets of randomized or Chrome
  fingerprints as JSONL — the exact input shape `cmd/fetch -fingerprints`
  consumes. Can be added as a sibling module if you want it in-repo.
```
