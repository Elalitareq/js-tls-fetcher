// Command server is the Go side of the Node binding. It reads newline-delimited
// JSON requests on stdin and writes newline-delimited JSON responses on stdout,
// performing each request with the fetcher package (input TLS fingerprint +
// optional proxy). One long-lived process handles many concurrent requests,
// correlated by "id".
//
// Request  : {"id":1,"url":"https://...","method":"GET","headers":{..},"body":"..",
//             "proxy":"http://user:pass@host:port","profile":"chrome_146",
//             "ja3":"771,...","user_agent":".."}
// Response : {"id":1,"status":200,"proto":"HTTP/2.0","headers":{..},
//             "body_b64":"..","ms":123,"err":""}
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"os"
	"sync"

	"tls-fetcher/fetcher"
)

type request struct {
	ID        int64             `json:"id"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Proxy     string            `json:"proxy"`
	Profile   string            `json:"profile"`
	JA3       string            `json:"ja3"`
	UserAgent string            `json:"user_agent"`
	Timeout   int               `json:"timeout"`
}

type response struct {
	ID      int64               `json:"id"`
	Status  int                 `json:"status"`
	Proto   string              `json:"proto"`
	Headers map[string][]string `json:"headers"`
	BodyB64 string              `json:"body_b64"`
	MS      int64               `json:"ms"`
	Err     string              `json:"err,omitempty"`
}

func main() {
	// Cache handlers by timeout (default 30). Handlers themselves cache clients
	// per fingerprint+proxy, so this reuses connections across requests.
	handlers := map[int]*fetcher.Handler{}
	var hmu sync.Mutex
	getHandler := func(t int) *fetcher.Handler {
		if t <= 0 {
			t = 30
		}
		hmu.Lock()
		defer hmu.Unlock()
		if h, ok := handlers[t]; ok {
			return h
		}
		h := fetcher.New(t, false)
		handlers[t] = h
		return h
	}

	out := json.NewEncoder(os.Stdout)
	var wmu sync.Mutex
	write := func(r response) {
		wmu.Lock()
		_ = out.Encode(r)
		wmu.Unlock()
	}

	var wg sync.WaitGroup
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024*1024), 32*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		buf := make([]byte, len(line))
		copy(buf, line)

		wg.Add(1)
		go func(raw []byte) {
			defer wg.Done()
			var req request
			if err := json.Unmarshal(raw, &req); err != nil {
				write(response{Err: "bad request json: " + err.Error()})
				return
			}
			res := getHandler(req.Timeout).Do(fetcher.Job{
				Method:  req.Method,
				URL:     req.URL,
				Headers: req.Headers,
				Body:    []byte(req.Body),
				Proxy:   req.Proxy,
				FP: fetcher.Fingerprint{
					Profile:   req.Profile,
					JA3:       req.JA3,
					UserAgent: req.UserAgent,
				},
			})
			resp := response{
				ID:      req.ID,
				Status:  res.Status,
				Proto:   res.Proto,
				Headers: res.Header,
				BodyB64: base64.StdEncoding.EncodeToString(res.Body),
				MS:      res.Duration.Milliseconds(),
			}
			if res.Err != nil {
				resp.Err = res.Err.Error()
			}
			write(resp)
		}(buf)
	}
	wg.Wait()
}
