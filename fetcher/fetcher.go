// Package fetcher is a request handler that performs HTTPS requests using a
// caller-supplied TLS fingerprint (a named browser profile OR a raw JA3 string)
// through a caller-supplied proxy (http/https/socks5, optional user:pass auth).
//
// It is built on github.com/bogdanfinn/tls-client, so each request reproduces a
// real browser's TLS fingerprint (JA3/JA4) AND HTTP/2 fingerprint. Clients are
// cached per (fingerprint + proxy) so repeated jobs reuse connections.
//
// IMPORTANT: keep this module isolated from refraction-networking/utls. Mixing
// both uTLS forks in one module can bump shared deps and make tls-client's fork
// silently fall back to a plain TLS 1.2 handshake.
package fetcher

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/http2"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	tls "github.com/bogdanfinn/utls"
)

// namedProfiles maps friendly names to tls-client browser profiles.
var namedProfiles = map[string]profiles.ClientProfile{
	"chrome_120":      profiles.Chrome_120,
	"chrome_131":      profiles.Chrome_131,
	"chrome_133":      profiles.Chrome_133,
	"chrome_144":      profiles.Chrome_144,
	"chrome_146":      profiles.Chrome_146,
	"firefox_132":     profiles.Firefox_132,
	"firefox_135":     profiles.Firefox_135,
	"firefox_148":     profiles.Firefox_148,
	"safari_16":       profiles.Safari_16_0,
	"safari_ios_17_0": profiles.Safari_IOS_17_0,
	"safari_ios_18_0": profiles.Safari_IOS_18_0,
	"safari_ios_18_5": profiles.Safari_IOS_18_5,
	"safari_ios_26_0": profiles.Safari_IOS_26_0,
}

// ProfileNames returns the supported named profiles (for help text / validation).
func ProfileNames() []string {
	out := make([]string, 0, len(namedProfiles))
	for k := range namedProfiles {
		out = append(out, k)
	}
	return out
}

// Fingerprint is the TLS identity to use for a request. Exactly one of Profile
// or JA3 should be set; if both are set, JA3 wins.
type Fingerprint struct {
	Profile   string // named browser profile, e.g. "chrome_146"
	JA3       string // raw JA3 string (custom fingerprint)
	UserAgent string // sent as the user-agent header
}

func (f Fingerprint) id() string {
	if f.JA3 != "" {
		s := sha1.Sum([]byte(f.JA3))
		return "ja3:" + hex.EncodeToString(s[:8])
	}
	return "profile:" + f.Profile
}

// Job is a single request to perform.
type Job struct {
	Method  string            // default "GET"
	URL     string            // required
	Headers map[string]string // extra/override headers (user-agent auto-added from FP)
	Body    []byte
	Proxy   string      // "" = direct; else http(s)://[user:pass@]host:port or socks5://...
	FP      Fingerprint // TLS fingerprint to use
}

// Result is the outcome of a Job.
type Result struct {
	Status   int
	Proto    string
	Header   map[string][]string
	Body     []byte
	Duration time.Duration
	Err      error
}

// Handler performs jobs and caches clients per (fingerprint + proxy).
type Handler struct {
	mu             sync.Mutex
	cache          map[string]tls_client.HttpClient
	timeoutSeconds int
	followRedirect bool
}

// New returns a Handler. timeoutSeconds<=0 defaults to 30.
func New(timeoutSeconds int, followRedirect bool) *Handler {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	return &Handler{
		cache:          map[string]tls_client.HttpClient{},
		timeoutSeconds: timeoutSeconds,
		followRedirect: followRedirect,
	}
}

// client returns a cached or newly-built client for the fingerprint+proxy pair.
func (h *Handler) client(fp Fingerprint, proxy string) (tls_client.HttpClient, error) {
	key := fp.id() + "|" + proxy
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.cache[key]; ok {
		return c, nil
	}

	profile, err := resolveProfile(fp)
	if err != nil {
		return nil, err
	}
	opts := []tls_client.HttpClientOption{
		tls_client.WithClientProfile(profile),
		tls_client.WithTimeoutSeconds(h.timeoutSeconds),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
	}
	if !h.followRedirect {
		opts = append(opts, tls_client.WithNotFollowRedirects())
	}
	if proxy != "" {
		opts = append(opts, tls_client.WithProxyUrl(proxy))
	}
	c, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, err
	}
	h.cache[key] = c
	return c, nil
}

// Do performs the job.
func (h *Handler) Do(job Job) Result {
	start := time.Now()
	c, err := h.client(job.FP, job.Proxy)
	if err != nil {
		return Result{Err: fmt.Errorf("build client: %w", err), Duration: time.Since(start)}
	}

	method := job.Method
	if method == "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if len(job.Body) > 0 {
		bodyReader = strings.NewReader(string(job.Body))
	}
	req, err := http.NewRequest(method, job.URL, bodyReader)
	if err != nil {
		return Result{Err: fmt.Errorf("new request: %w", err), Duration: time.Since(start)}
	}
	req.Header = buildHeaders(job.FP, job.Headers)

	resp, err := c.Do(req)
	if err != nil {
		return Result{Err: err, Duration: time.Since(start)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	return Result{
		Status:   resp.StatusCode,
		Proto:    resp.Proto,
		Header:   resp.Header,
		Body:     body,
		Duration: time.Since(start),
	}
}

// buildHeaders merges the fingerprint's User-Agent with caller headers and adds
// a reasonable browser header order.
func buildHeaders(fp Fingerprint, extra map[string]string) http.Header {
	h := http.Header{}
	if fp.UserAgent != "" {
		h["user-agent"] = []string{fp.UserAgent}
	}
	h["accept"] = []string{"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"}
	h["accept-language"] = []string{"en-US,en;q=0.9"}
	h["accept-encoding"] = []string{"gzip, deflate, br"}
	for k, v := range extra {
		h[strings.ToLower(k)] = []string{v}
	}
	h[http.HeaderOrderKey] = []string{
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
		"upgrade-insecure-requests", "user-agent", "accept",
		"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest",
		"accept-encoding", "accept-language", "cookie",
	}
	h[http.PHeaderOrderKey] = []string{":method", ":authority", ":scheme", ":path"}
	return h
}

// resolveProfile turns a Fingerprint into a tls-client ClientProfile.
func resolveProfile(fp Fingerprint) (profiles.ClientProfile, error) {
	if fp.JA3 != "" {
		return customProfileFromJA3(fp.JA3)
	}
	p, ok := namedProfiles[fp.Profile]
	if !ok {
		return profiles.ClientProfile{}, fmt.Errorf("unknown profile %q (have: %s)", fp.Profile, strings.Join(ProfileNames(), ", "))
	}
	return p, nil
}

// customProfileFromJA3 builds a full ClientProfile from a raw JA3 string.
// JA3 does not encode signature algorithms, TLS versions, key-share curves,
// ALPN/ALPS, ECH or cert-compression, so we fill those with modern-Chrome-like
// defaults (adjust here if you feed non-Chrome JA3s and need exact parity).
func customProfileFromJA3(ja3 string) (profiles.ClientProfile, error) {
	supportedSignatureAlgorithms := []string{
		"ECDSAWithP256AndSHA256", "PSSWithSHA256", "PKCS1WithSHA256",
		"ECDSAWithP384AndSHA384", "PSSWithSHA384", "PKCS1WithSHA384",
		"PSSWithSHA512", "PKCS1WithSHA512",
	}
	supportedVersions := []string{"GREASE", "1.3", "1.2"}
	keyShareCurves := []string{"GREASE", "X25519Kyber768", "X25519"}
	alpn := []string{"h2", "http/1.1"}
	alps := []string{"h2"}
	echCandidates := []tls_client.CandidateCipherSuites{
		{KdfId: "HKDF_SHA256", AeadId: "AEAD_AES_128_GCM"},
		{KdfId: "HKDF_SHA256", AeadId: "AEAD_CHACHA20_POLY1305"},
	}
	candidatePayloads := []uint16{128, 160, 192, 224}
	certCompression := []string{"brotli"}

	specFactory, err := tls_client.GetSpecFactoryFromJa3String(
		ja3, supportedSignatureAlgorithms, nil, supportedVersions, keyShareCurves,
		alpn, alps, echCandidates, candidatePayloads, certCompression, 0,
	)
	if err != nil {
		return profiles.ClientProfile{}, fmt.Errorf("parse ja3: %w", err)
	}

	// Chrome-like HTTP/2 fingerprint to accompany the custom TLS layer.
	settings := map[http2.SettingID]uint32{
		http2.SettingHeaderTableSize:   65536,
		http2.SettingEnablePush:        0,
		http2.SettingInitialWindowSize: 6291456,
		http2.SettingMaxHeaderListSize: 262144,
	}
	settingsOrder := []http2.SettingID{
		http2.SettingHeaderTableSize, http2.SettingEnablePush,
		http2.SettingInitialWindowSize, http2.SettingMaxHeaderListSize,
	}
	pseudoHeaderOrder := []string{":method", ":authority", ":scheme", ":path"}
	connectionFlow := uint32(15663105)

	return profiles.NewClientProfile(tls.ClientHelloID{
		Client:      "CustomJA3",
		Version:     "1",
		Seed:        nil,
		SpecFactory: specFactory,
	}, settings, settingsOrder, pseudoHeaderOrder, connectionFlow, nil, nil, 0, false, nil, nil, 0, nil, false), nil
}
