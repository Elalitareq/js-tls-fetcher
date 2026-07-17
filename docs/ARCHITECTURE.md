# Architecture & Design

## Purpose

Many servers identify clients not by what they *say* they are (the `User-Agent`
header) but by *how they speak TLS and HTTP/2* — the byte-level shape of the TLS
ClientHello and the HTTP/2 connection preamble. Two requests can send the same
`User-Agent: ...Chrome...` yet be trivially distinguishable if one is really Go's
`net/http` and the other is real Chrome.

**tls-fetcher exists to make a request that is consistent all the way down:** the
TLS fingerprint, the HTTP/2 fingerprint, and the headers all match a chosen real
browser (or a chosen raw fingerprint), optionally routed through a proxy. It's
the request engine you point at fingerprint-aware endpoints — for scraping,
automation, or for testing the fingerprint defenses of a service you operate.

## The three fingerprints a request exposes

1. **TLS fingerprint (JA3 / JA4).** Derived from the ClientHello: TLS version,
   cipher-suite list, extension list, elliptic curves, EC point formats. JA3 is
   an MD5 of those fields (order-sensitive); JA4 is a newer, structured hash that
   sorts extensions (because modern Chrome shuffles extension order every
   connection, its JA3 changes constantly while JA4 stays stable).
2. **HTTP/2 fingerprint ("Akamai").** The HTTP/2 SETTINGS values and their order,
   the connection WINDOW_UPDATE increment, header PRIORITY info, and the
   pseudo-header order (`:method :authority :scheme :path` for Chrome). A real
   JA3 with a Go-default HTTP/2 preamble is a dead giveaway.
3. **Header set + order.** Real browsers send a specific set of headers
   (including `sec-ch-ua*`, `sec-fetch-*` client hints for Chrome) in a specific
   order. Order is part of the signal.

tls-fetcher gets all three right by delegating to
[`bogdanfinn/tls-client`](https://github.com/bogdanfinn/tls-client), which pairs
uTLS (TLS layer) with an HTTP/2 stack whose SETTINGS/window/priority match each
browser profile, and by letting the caller set header + pseudo-header order.

## Components and data flow

```
                    ┌────────────────────────────────────────────┐
   Go callers  ───► │ fetcher (Go library)                       │
                    │  Handler.Do(Job{URL, Proxy, FP})           │
                    │   • resolveProfile: named profile OR        │
                    │     customProfileFromJA3(ja3)               │
                    │   • client cache keyed by (fingerprint|proxy)│
                    │   • tls-client performs TLS+HTTP2+request    │
                    └───────────────▲────────────────────────────┘
                                     │
        cmd/fetch (CLI) ─────────────┤  rotate fingerprints + proxies over a URL
                                     │
        cmd/server ──────────────────┘  NDJSON over stdio
             ▲
             │ spawn + JSON lines (id-correlated)
   Node  ────┘
   node/index.js  TlsClient.request({url, profile|ja3, proxy, headers, body})
```

- **`fetcher/fetcher.go`** — the core. `Fingerprint{Profile | JA3, UserAgent}`,
  `Job{Method, URL, Headers, Body, Proxy, FP}`, `Handler` with a mutex-guarded
  cache of `tls-client` clients keyed by `fingerprint-id | proxy` so repeated
  jobs reuse connections. `customProfileFromJA3` turns a raw JA3 into a full
  `tls-client` `ClientProfile` via `GetSpecFactoryFromJa3String`, filling
  JA3-unrepresentable fields with Chrome-like defaults and attaching a Chrome
  HTTP/2 profile.
- **`cmd/fetch`** — loads a JSONL fingerprint list (`{profile|ja3, user_agent}`
  per line — the shape emitted by the companion generators) and a proxy list,
  then fires `n` requests with round-robin or random rotation using a worker
  pool, writing per-request results as JSONL.
- **`cmd/server`** — the Node binding's backend. One JSON request per stdin line
  → runs on a goroutine → one JSON response per stdout line (base64 body),
  correlated by `id`. Long-lived; handles concurrency.
- **`node/index.js`** — spawns `cmd/server`, writes request lines, resolves the
  matching Promise when the response id comes back. Exposes `TlsClient` with
  `request()` returning `{status, proto, headers, ms, ok, error, body, text(),
  json()}`.

## Why the Node binding is a subprocess, not WASM

The request's fingerprint is decided by the exact bytes written to a **raw TCP
socket** during the TLS handshake. WASM sandboxes (`js/wasm` and Node's WASI)
don't expose raw sockets — outbound traffic goes through the host's `fetch`,
which runs the host's (Node/V8) TLS stack. Compiling the Go handler to WASM
would therefore inherit Node's fingerprint and impersonate nothing. Shipping the
Go code as a **native binary** and talking to it over stdio (the CycleTLS
approach) keeps the real handshake. Pure Go (`CGO_ENABLED=0`) makes the binary
trivially cross-compilable, so distribution stays simple.

## Verification methodology

Because a fingerprint can be silently wrong (wrong profile applied, a dependency
fallback, an intercepting proxy), development verified the *actual bytes on the
wire*, two ways:

1. **Local ClientHello capture.** A raw TCP listener on `127.0.0.1` accepts the
   connection, reads the first TLS record, and parses the ClientHello into a JA3.
   Pointing the handler at it confirms, for example, that a *custom input JA3*
   reproduces the exact cipher order requested, and that a named profile emits an
   authentic Chrome hello (TLS 1.3, `key_share`, `supported_versions`, ECH,
   `X25519MLKEM768`, GREASE). This bypasses any egress proxy.
2. **End-to-end Node test** (`node/test.js`) against a local HTTP echo server:
   named profile, raw JA3, POST body, custom header forwarding, and 25
   concurrent id-correlated requests.

Note on external checkers: if the machine's outbound HTTPS is MITM'd by a
TLS-terminating proxy, a service like `tls.peet.ws` sees the proxy's handshake,
not the handler's — so all profiles look identical there. That's an environment
artifact, not a bug; verify via local capture in that case.

## Dependency note (important)

The module deliberately uses **only** `bogdanfinn/tls-client` (and its
`fhttp`/`utls` forks). Do **not** add `refraction-networking/utls` here — two
uTLS forks in one module can bump shared `golang.org/x/crypto` / `x/net` versions
and make tls-client's fork fall back to a stdlib TLS 1.2 handshake, quietly
destroying the fingerprint. Keep refraction-utls tooling (e.g. JA3 generators) in
a separate module.

## Extensibility

- **More browsers:** add entries to `namedProfiles` as tls-client ships them.
- **Non-Chrome custom JA3:** adjust the defaults in `customProfileFromJA3`
  (sig-algs, versions, curves, ALPN/ALPS, ECH, HTTP/2 settings) to match the
  target browser family.
- **Retries / backoff / streaming / per-request cookie jars:** natural additions
  in the `fetcher` layer and surfaced through both the CLI and the Node API.
