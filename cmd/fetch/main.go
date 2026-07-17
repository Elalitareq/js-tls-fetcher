// Command fetch drives the fetcher package: it performs many requests to a URL,
// rotating over a set of input TLS fingerprints and a set of proxies.
//
// Fingerprints come from a JSONL file (one object per line) with any of:
//
//	{"profile":"chrome_146","user_agent":"..."}   // named browser profile
//	{"ja3":"771,4865-...","user_agent":"..."}     // raw JA3 (custom fingerprint)
//
// This is exactly the shape produced by the companion generators, so you can
// feed fingerprints.jsonl / chrome_fingerprints.jsonl straight in.
//
// Proxies come from a text file, one per line:
//
//	http://user:pass@host:port
//	socks5://host:1080
//
// Usage:
//
//	go run ./cmd/fetch -url https://tls.peet.ws/api/all \
//	    -fingerprints chrome_fingerprints.jsonl -proxies proxies.txt \
//	    -n 100 -c 20 -out results.jsonl
//
//	# single fingerprint, single proxy:
//	go run ./cmd/fetch -url https://example.com -profile chrome_146 -proxy http://user:pass@host:port
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"tls-fetcher/fetcher"
)

type fpLine struct {
	Profile   string `json:"profile"`
	JA3       string `json:"ja3"`
	UserAgent string `json:"user_agent"`
}

type outLine struct {
	I      int    `json:"i"`
	FP     string `json:"fp"`
	Proxy  string `json:"proxy"`
	Status int    `json:"status"`
	Proto  string `json:"proto"`
	MS     int64  `json:"ms"`
	Bytes  int    `json:"bytes"`
	Err    string `json:"err,omitempty"`
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

func loadFingerprints(path string) ([]fetcher.Fingerprint, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	var fps []fetcher.Fingerprint
	for _, l := range lines {
		var fl fpLine
		if json.Unmarshal([]byte(l), &fl) != nil {
			continue
		}
		if fl.Profile == "" && fl.JA3 == "" {
			continue
		}
		fps = append(fps, fetcher.Fingerprint{Profile: fl.Profile, JA3: fl.JA3, UserAgent: fl.UserAgent})
	}
	if len(fps) == 0 {
		return nil, fmt.Errorf("no usable fingerprints in %s", path)
	}
	return fps, nil
}

func main() {
	url := flag.String("url", "", "target URL (required)")
	fpFile := flag.String("fingerprints", "", "JSONL file of fingerprints (profile/ja3/user_agent)")
	profile := flag.String("profile", "", "single named profile (if no -fingerprints)")
	ja3 := flag.String("ja3", "", "single raw JA3 string (if no -fingerprints)")
	ua := flag.String("ua", "", "user-agent for single-fingerprint mode")
	proxyFile := flag.String("proxies", "", "text file of proxies, one per line")
	proxy := flag.String("proxy", "", "single proxy (if no -proxies)")
	n := flag.Int("n", 0, "number of requests (default: number of fingerprints, min 1)")
	c := flag.Int("c", 10, "concurrency")
	rotate := flag.String("rotate", "roundrobin", "fingerprint/proxy rotation: roundrobin|random")
	timeout := flag.Int("timeout", 30, "per-request timeout seconds")
	seed := flag.Int64("seed", time.Now().UnixNano(), "PRNG seed for random rotation")
	outFile := flag.String("out", "", "results JSONL file (default: summary only)")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "-url is required")
		os.Exit(1)
	}

	// Fingerprints.
	var fps []fetcher.Fingerprint
	var err error
	if *fpFile != "" {
		fps, err = loadFingerprints(*fpFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "load fingerprints:", err)
			os.Exit(1)
		}
	} else if *profile != "" || *ja3 != "" {
		fps = []fetcher.Fingerprint{{Profile: *profile, JA3: *ja3, UserAgent: *ua}}
	} else {
		fmt.Fprintln(os.Stderr, "provide -fingerprints, or -profile / -ja3")
		os.Exit(1)
	}

	// Proxies (empty string = direct).
	var proxies []string
	if *proxyFile != "" {
		proxies, err = readLines(*proxyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "load proxies:", err)
			os.Exit(1)
		}
	} else if *proxy != "" {
		proxies = []string{*proxy}
	} else {
		proxies = []string{""}
	}

	total := *n
	if total <= 0 {
		total = len(fps)
		if total < 1 {
			total = 1
		}
	}

	rng := rand.New(rand.NewSource(*seed))
	pick := func(i, mod int) int {
		if *rotate == "random" {
			return rng.Intn(mod)
		}
		return i % mod
	}

	h := fetcher.New(*timeout, false)

	var out *bufio.Writer
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "create out:", err)
			os.Exit(1)
		}
		defer f.Close()
		out = bufio.NewWriter(f)
		defer out.Flush()
	}

	type work struct{ i int }
	jobs := make(chan work, *c)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, fail int
	var totalMS int64
	start := time.Now()

	worker := func() {
		defer wg.Done()
		for w := range jobs {
			fp := fps[pick(w.i, len(fps))]
			px := proxies[pick(w.i, len(proxies))]
			res := h.Do(fetcher.Job{URL: *url, Proxy: px, FP: fp})

			ol := outLine{
				I: w.i, FP: fpLabel(fp), Proxy: px,
				Status: res.Status, Proto: res.Proto,
				MS: res.Duration.Milliseconds(), Bytes: len(res.Body),
			}
			mu.Lock()
			totalMS += ol.MS
			if res.Err != nil {
				ol.Err = res.Err.Error()
				fail++
			} else if res.Status >= 200 && res.Status < 400 {
				ok++
			} else {
				fail++
			}
			if out != nil {
				line, _ := json.Marshal(ol)
				out.Write(line)
				out.WriteByte('\n')
			}
			mu.Unlock()
		}
	}

	for i := 0; i < *c; i++ {
		wg.Add(1)
		go worker()
	}
	for i := 0; i < total; i++ {
		jobs <- work{i}
	}
	close(jobs)
	wg.Wait()

	elapsed := time.Since(start)
	avg := int64(0)
	if total > 0 {
		avg = totalMS / int64(total)
	}
	fmt.Printf("requests=%d ok=%d fail=%d fingerprints=%d proxies=%d avg=%dms wall=%s\n",
		total, ok, fail, len(fps), len(proxies), avg, elapsed.Round(time.Millisecond))
	if *outFile != "" {
		fmt.Printf("per-request results written to %s\n", *outFile)
	}
}

func fpLabel(fp fetcher.Fingerprint) string {
	if fp.JA3 != "" {
		return "ja3"
	}
	return fp.Profile
}
