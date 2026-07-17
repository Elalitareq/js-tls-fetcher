// js-tls-fetcher — Node client over a Go (uTLS/tls-client) helper process.
//
// Make HTTPS requests with a chosen TLS fingerprint (a named browser profile
// OR a raw JA3 string) through an optional proxy. The Go child process does the
// real TLS/HTTP2 work; Node talks to it over newline-delimited JSON on stdio.
//
// Usage:
//   import { TlsClient } from "js-tls-fetcher";
//   const client = new TlsClient();               // spawns the Go helper
//   const res = await client.request({
//     url: "https://tls.peet.ws/api/all",
//     profile: "chrome_146",                       // or: ja3: "771,4865-..."
//     proxy: "http://user:pass@host:port",         // optional
//     userAgent: "Mozilla/5.0 ...",                // optional
//     headers: { "accept-language": "en-US" },     // optional
//   });
//   console.log(res.status, res.text());
//   client.close();

import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { existsSync } from "node:fs";

const __dirname = dirname(fileURLToPath(import.meta.url));

function defaultBinaryPath() {
  const exe = process.platform === "win32" ? "tlsfetch-server.exe" : "tlsfetch-server";
  return join(__dirname, "bin", exe);
}

export class TlsClient {
  /**
   * @param {object} [opts]
   * @param {string} [opts.binaryPath] path to the Go helper binary (default: ./bin/tlsfetch-server)
   */
  constructor(opts = {}) {
    const bin = opts.binaryPath || defaultBinaryPath();
    if (!existsSync(bin)) {
      throw new Error(
        `Go helper binary not found at ${bin}. Build it first: npm run build ` +
        `(requires Go), or pass { binaryPath }.`
      );
    }
    this._nextId = 1;
    this._pending = new Map();
    this._closed = false;

    this._proc = spawn(bin, [], { stdio: ["pipe", "pipe", "inherit"] });
    this._proc.on("exit", (code) => {
      this._closed = true;
      const err = new Error(`tls-fetcher helper exited (code ${code})`);
      for (const { reject } of this._pending.values()) reject(err);
      this._pending.clear();
    });

    this._rl = createInterface({ input: this._proc.stdout });
    this._rl.on("line", (line) => {
      if (!line) return;
      let msg;
      try {
        msg = JSON.parse(line);
      } catch {
        return;
      }
      const p = this._pending.get(msg.id);
      if (!p) return;
      this._pending.delete(msg.id);
      p.resolve(makeResponse(msg));
    });
  }

  /**
   * Perform a request.
   * @param {object} req
   * @param {string} req.url
   * @param {string} [req.method="GET"]
   * @param {Object<string,string>} [req.headers]
   * @param {string} [req.body]
   * @param {string} [req.proxy]        http(s)://[user:pass@]host:port | socks5://host:port
   * @param {string} [req.profile]      named profile, e.g. "chrome_146"
   * @param {string} [req.ja3]          raw JA3 string (overrides profile)
   * @param {string} [req.userAgent]
   * @param {number} [req.timeout=30]   seconds
   * @returns {Promise<Response>}
   */
  request(req) {
    if (this._closed) return Promise.reject(new Error("client is closed"));
    if (!req || !req.url) return Promise.reject(new Error("request.url is required"));

    const id = this._nextId++;
    const payload = {
      id,
      url: req.url,
      method: req.method || "GET",
      headers: req.headers || {},
      body: req.body || "",
      proxy: req.proxy || "",
      profile: req.profile || "",
      ja3: req.ja3 || "",
      user_agent: req.userAgent || "",
      timeout: req.timeout || 0,
    };
    return new Promise((resolve, reject) => {
      this._pending.set(id, { resolve, reject });
      this._proc.stdin.write(JSON.stringify(payload) + "\n", (err) => {
        if (err) {
          this._pending.delete(id);
          reject(err);
        }
      });
    });
  }

  close() {
    if (this._closed) return;
    this._closed = true;
    try {
      this._proc.stdin.end();
    } catch {}
    this._proc.kill();
  }
}

function makeResponse(msg) {
  const bodyBuf = Buffer.from(msg.body_b64 || "", "base64");
  return {
    status: msg.status,
    proto: msg.proto,
    headers: msg.headers || {},
    ms: msg.ms,
    error: msg.err || null,
    ok: !msg.err && msg.status >= 200 && msg.status < 400,
    body: bodyBuf,
    text() {
      return bodyBuf.toString("utf8");
    },
    json() {
      return JSON.parse(bodyBuf.toString("utf8"));
    },
  };
}

export default TlsClient;
