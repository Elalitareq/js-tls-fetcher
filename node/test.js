// End-to-end test: Node -> Go helper -> request -> response.
// Uses a local HTTP echo server so the test is self-contained.
import http from "node:http";
import { TlsClient } from "./index.js";

const server = http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    res.setHeader("content-type", "application/json");
    res.end(
      JSON.stringify({
        method: req.method,
        path: req.url,
        user_agent: req.headers["user-agent"] || null,
        accept_language: req.headers["accept-language"] || null,
        got_body: body,
      })
    );
  });
});

await new Promise((r) => server.listen(0, "127.0.0.1", r));
const port = server.address().port;
const base = `http://127.0.0.1:${port}`;

const client = new TlsClient();
let failures = 0;
const check = (name, cond) => {
  console.log(`${cond ? "PASS" : "FAIL"}  ${name}`);
  if (!cond) failures++;
};

// 1) named profile + custom UA + extra header
const r1 = await client.request({
  url: base + "/hello",
  profile: "chrome_146",
  userAgent: "Mozilla/5.0 TEST-CHROME",
  headers: { "accept-language": "fr-FR,fr;q=0.9" },
});
const j1 = r1.json();
check("status 200 (named profile)", r1.status === 200);
check("path echoed", j1.path === "/hello");
check("user-agent forwarded", j1.user_agent === "Mozilla/5.0 TEST-CHROME");
check("custom header forwarded", j1.accept_language === "fr-FR,fr;q=0.9");

// 2) raw JA3 input + POST body
const r2 = await client.request({
  url: base + "/post",
  method: "POST",
  ja3: "771,4867-4865-4866-49195-49199,0-23-65281-10-11-16-5-13-43-51-45-18,29-23-24,0",
  userAgent: "Mozilla/5.0 TEST-JA3",
  body: "payload=42",
});
const j2 = r2.json();
check("status 200 (raw JA3)", r2.status === 200);
check("method POST", j2.method === "POST");
check("body forwarded", j2.got_body === "payload=42");

// 3) concurrent requests keep their ids straight
const many = await Promise.all(
  Array.from({ length: 25 }, (_, i) =>
    client.request({ url: `${base}/n/${i}`, profile: "chrome_133" }).then((r) => r.json().path)
  )
);
check("25 concurrent requests correlated", many.every((p, i) => p === `/n/${i}`));

client.close();
server.close();
console.log(failures === 0 ? "\nALL PASS" : `\n${failures} FAILED`);
process.exit(failures === 0 ? 0 : 1);
