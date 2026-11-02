// kol-playplane is a localhost-only reverse proxy for a customizable KoLmafia UI.
// It adds /play/ but leaves KoLmafia, Xpra, WebSockets, and cookies to the
// existing Caddy gateway. No game-state requests are intercepted or retried.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newHandler(target *url.URL, assets string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("backend unavailable for %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "KoLmafia gateway unavailable", http.StatusBadGateway)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "kol-playplane")
	})
	mux.HandleFunc("GET /play", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/play/", http.StatusTemporaryRedirect)
	})
	mux.Handle("/play/", http.StripPrefix("/play/", http.FileServer(http.Dir(assets))))
	mux.HandleFunc("/", proxy.ServeHTTP)
	return mux
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18080", "loopback HTTP listener")
	upstream := flag.String("upstream", "http://127.0.0.1:8080", "local Caddy gateway")
	assets := flag.String("assets", "playplane/web", "live-editable play-plane assets")
	flag.Parse()
	if !loopbackHost(*listen) {
		log.Fatal("listen must be an explicit loopback IP:port (e.g. 127.0.0.1:18080)")
	}
	target, err := url.Parse(*upstream)
	if err != nil || target.Scheme != "http" || target.User != nil || target.Path != "" || target.RawQuery != "" || !loopbackHost(target.Host) {
		log.Fatal("upstream must be a local http://127.0.0.1:PORT URL with no path or credentials")
	}
	if info, err := os.Stat(*assets); err != nil || !info.IsDir() {
		log.Fatalf("assets directory unavailable: %s", *assets)
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           newHandler(target, *assets),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		// No WriteTimeout: upgraded Xpra WebSocket connections are long-lived.
	}
	log.Printf("KoLmafia play plane: http://%s/play/ -> %s", *listen, target)
	log.Fatal(srv.ListenAndServe())
}
