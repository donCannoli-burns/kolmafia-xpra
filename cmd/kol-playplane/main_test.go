package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOnlyLoopback(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:18080", true}, {"[::1]:18080", true},
		{"0.0.0.0:18080", false}, {"192.168.1.2:18080", false},
		{"localhost:18080", false}, {"127.0.0.1", false},
	} {
		if got := loopbackHost(tc.addr); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestRoutesAndSinglePost(t *testing.T) {
	var count int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, r.Method+" "+r.URL.RequestURI())
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("custom-play-plane"), 0600); err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(newHandler(target, assets))
	defer app.Close()
	for _, tc := range []struct {
		method, path, body string
		status             int
		contains           string
	}{
		{"GET", "/healthz", "", 200, "kol-playplane"},
		{"GET", "/play/", "", 200, "custom-play-plane"},
		{"GET", "/xpra/", "", 202, "GET /xpra/"},
		{"POST", "/choice.php?whichchoice=1", "test", 202, "POST /choice.php?whichchoice=1"},
	} {
		req, _ := http.NewRequest(tc.method, app.URL+tc.path, strings.NewReader(tc.body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		bs, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status || !strings.Contains(string(bs), tc.contains) {
			t.Errorf("%s %s: status=%d body=%q", tc.method, tc.path, resp.StatusCode, bs)
		}
	}
	if atomic.LoadInt32(&count) != 2 {
		t.Errorf("backend saw %d requests, want 2", count)
	}
}
