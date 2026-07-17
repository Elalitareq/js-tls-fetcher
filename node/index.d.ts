// Type definitions for js-tls-fetcher.

/** Named browser profiles compiled into the Go helper. A raw `ja3` overrides these. */
export type Profile =
  | "chrome_120"
  | "chrome_131"
  | "chrome_133"
  | "chrome_144"
  | "chrome_146"
  | "firefox_132"
  | "firefox_135"
  | "firefox_148"
  | "safari_16"
  | "safari_ios_17_0"
  | "safari_ios_18_0"
  | "safari_ios_18_5"
  | "safari_ios_26_0";

export interface TlsClientOptions {
  /** Path to the Go helper binary. Defaults to the bundled `node/bin/tlsfetch-server`. */
  binaryPath?: string;
}

export interface TlsRequest {
  url: string;
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  /** `http(s)://[user:pass@]host:port` or `socks5://host:port`. */
  proxy?: string;
  /** Named profile. Ignored when `ja3` is set. */
  profile?: Profile | (string & {});
  /** Raw JA3 string. Overrides `profile`. */
  ja3?: string;
  userAgent?: string;
  /** Seconds. Defaults to the helper's own default (30). */
  timeout?: number;
}

export interface TlsResponse {
  status: number;
  /** Negotiated protocol, e.g. "HTTP/2.0". */
  proto: string;
  headers: Record<string, string | string[]>;
  /** Round-trip time in milliseconds. */
  ms: number;
  /** Transport-level error, or null on success. */
  error: string | null;
  /** True when there is no error and status is 2xx/3xx. */
  ok: boolean;
  body: Buffer;
  text(): string;
  json(): unknown;
}

export declare class TlsClient {
  constructor(opts?: TlsClientOptions);
  request(req: TlsRequest): Promise<TlsResponse>;
  /** Terminate the helper process. Pending requests reject. */
  close(): void;
}

export default TlsClient;
