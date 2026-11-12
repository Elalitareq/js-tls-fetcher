# js-tls-fetcher

[![CI](https://github.com/Elalitareq/js-tls-fetcher/actions/workflows/ci.yml/badge.svg)](https://github.com/Elalitareq/js-tls-fetcher/actions/workflows/ci.yml)

A request handler that performs HTTPS requests with a **chosen TLS fingerprint**
— a named browser profile *or* a raw JA3 string — through an **optional proxy**
(http/https/socks5, with auth). Real Chrome/Firefox/Safari TLS **and** HTTP/2
fingerprints, driven by [bogdanfinn/tls-client](https://github.com/bogdanfinn/tls-client)
(uTLS under the hood).

Ships three ways to use it:

1. **Go library** — `fetcher` package.
2. **Go CLI** — `cmd/fetch`, rotates fingerprints + proxies over a target.
3. **Node package** — `node/`, a JS API that runs requests **through a Go helper
   process** (the working, real-fingerprint way to use this from Node).

## Purpose

Servers increasingly identify clients by *how they speak TLS and HTTP/2* — the
byte shape of the TLS ClientHello (JA3/JA4) and the HTTP/2 preamble — not just by
the `User-Agent` header. tls-fetcher makes a request that is consistent all the
way down: TLS fingerprint, HTTP/2 fingerprint, and headers all match a chosen
real browser (or a raw JA3 you supply), optionally through a proxy. Use it as the
request engine for fingerprint-aware endpoints, or to test the fingerprint
defenses of a service you run.

## Documentation

- [`CLAUDE.md`](CLAUDE.md) — orientation + commands + gotchas (also for Claude Code).
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — design, data flow, the three
  fingerprints, why-not-WASM, and how the fingerprints were verified.

## Why a Go subprocess for Node (and not WASM)

TLS fingerprinting works by opening a **raw TCP socket** and writing a
hand-crafted TLS ClientHello. A WASM sandbox — both `js/wasm` and Node's WASI —
has **no raw sockets**; any network call is forced through the host's `fetch`,
which uses Node/V8's own TLS stack and **fixes the JA3 to Node's fingerprint**.
So Go→WASM can run, but it cannot impersonate — the feature is impossible there.

The working pattern (also how CycleTLS and node-tls-client operate) is to keep
the Go code as a **native binary** and have Node talk to it. This package does
that over newline-delimited JSON on stdio: one long-lived Go process, many
concurrent requests correlated by id. Pure Go, no CGO — cross-compiles to
darwin/arm64, linux, windows.

## Install

```bash
# From GitHub (works today, no registry needed):
npm install github:Elalitareq/js-tls-fetcher

# Pin to a release tag (recommended for servers):
npm install github:Elalitareq/js-tls-fetcher#v0.1.0

# From npm, once a v-tag has been published:
npm install js-tls-fetcher
```

On install, a postinstall step obtains the native Go helper for your platform:

1. builds it with `go build` if a Go toolchain is present, else
2. downloads the prebuilt binary for your OS/arch from this repo's
   [Releases](https://github.com/Elalitareq/js-tls-fetcher/releases).

It never hard-fails the install. If both routes fail (no Go **and** no matching
release asset), run `npm run build` manually or drop a binary into
`node_modules/js-tls-fetcher/node/bin/`. Supported: darwin/linux/windows on
x64 or arm64.

## Node quick start

```js
import { TlsClient } from "js-tls-fetcher";

const client = new TlsClient();                 // spawns the Go helper

// named browser profile
const a = await client.request({
  url: "https://tls.peet.ws/api/all",
  profile: "chrome_146",
  proxy: "http://user:pass@host:port",          // optional
});
console.log(a.status, a.json());

// raw JA3 (custom fingerprint) + POST
const b = await client.request({
  url: "https://example.com/api",
  method: "POST",
  ja3: "771,4865-4866-4867-49195-49199,0-23-65281-10-11-16-5-13-43-51-45-18,29-23-24,0",
  userAgent: "Mozilla/5.0 ...",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ hello: "world" }),
});
console.log(b.status, b.text());

client.close();
```

Response object: `{ status, proto, headers, ms, ok, error, body(Buffer), text(), json() }`.

`npm test` runs an end-to-end test (Node → Go → local echo server): named
profile, raw JA3, POST body, custom headers, and 25 correlated concurrent
requests.

### Distribution note

The helper is a native binary, so ship one per platform (or `go build` on
install). Cross-build examples:

```bash
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -o node/bin/tlsfetch-server        ./cmd/server
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o node/bin/tlsfetch-server        ./cmd/server
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o node/bin/tlsfetch-server.exe    ./cmd/server
```

## Go library

```go
import "tls-fetcher/fetcher"

h := fetcher.New(30, false) // timeout seconds, followRedirect
res := h.Do(fetcher.Job{
    URL:   "https://example.com",
    Proxy: "socks5://host:1080",              // optional
    FP:    fetcher.Fingerprint{Profile: "chrome_146", UserAgent: "..."},
    // or FP: fetcher.Fingerprint{JA3: "771,4865-...", UserAgent: "..."},
})
fmt.Println(res.Status, res.Proto, len(res.Body), res.Err)
```

Clients are cached per (fingerprint + proxy) so repeated jobs reuse connections.

Named profiles: `chrome_120/131/133/144/146`, `firefox_132/135/148`,
`safari_16`, `safari_ios_17_0/18_0/18_5/26_0`. A raw `JA3` overrides the profile;
JA3 fields it can't encode (sig-algs, versions, key-share curves, ALPN/ALPS, ECH,
cert-compression) are filled with modern-Chrome-like defaults — edit
`customProfileFromJA3` if you feed non-Chrome JA3s and need exact parity.

## Go CLI — rotate fingerprints + proxies

Feed the fingerprint files from the companion generators straight in (each line
has `ja3`/`profile` + `user_agent`):

```bash
go run ./cmd/fetch -url https://tls.peet.ws/api/all \
  -fingerprints chrome_fingerprints.jsonl \
  -proxies proxies.txt \
  -n 100 -c 20 -rotate random -out results.jsonl
```

`proxies.txt` is one proxy per line (`http://user:pass@host:port`,
`socks5://host:1080`). Single-shot mode: `-profile chrome_146 -proxy ...` or
`-ja3 "771,..." -proxy ...`.

## Important: keep this module isolated

Do **not** add `refraction-networking/utls` to this module. Two uTLS forks in one
module can bump shared deps and make tls-client's fork silently fall back to a
plain TLS 1.2 handshake. The JA3-generator tools live in their own module for
exactly this reason.

## License

MIT, see [LICENSE](LICENSE). Built on
[bogdanfinn/tls-client](https://github.com/bogdanfinn/tls-client) and its
`fhttp`/`utls` forks; their licenses are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). This product includes software
developed by Bogdan Finn.
