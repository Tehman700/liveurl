// Command demoapp is a tiny test target for liveurl: a couple of HTML
// pages (to exercise the snapshot cache) and a webhook endpoint that logs
// what it receives (to exercise buffering/replay).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

const indexHTML = `<!doctype html><html><head><title>demoapp</title>
<link rel="stylesheet" href="/style.css"></head>
<body>
<h1>liveurl demo app</h1>
<p>If you can see this through your tunnel, the live proxy path works.</p>
<p><a href="/about">about page</a></p>
<script>console.log("demoapp loaded")</script>
</body></html>`

const aboutHTML = `<!doctype html><html><head><title>about — demoapp</title>
<link rel="stylesheet" href="/style.css"></head>
<body><h1>About</h1><p>Second page, for testing multi-page snapshots.</p>
<p><a href="/">home</a></p></body></html>`

const styleCSS = `body{font-family:system-ui,sans-serif;max-width:40rem;margin:3rem auto;padding:0 1rem}`

func main() {
	port := "3000"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	})
	mux.HandleFunc("GET /about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(aboutHTML))
	})
	mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write([]byte(styleCSS))
	})
	mux.HandleFunc("POST /webhooks/stripe", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var pretty any
		if json.Unmarshal(body, &pretty) == nil {
			b, _ := json.MarshalIndent(pretty, "", "  ")
			body = b
		}
		log.Printf("webhook received: buffered=%s original-ts=%s body=%s",
			r.Header.Get("X-Liveurl-Buffered"), r.Header.Get("X-Liveurl-Original-Timestamp"), body)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("POST /webhooks/fail", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		log.Printf("webhook /webhooks/fail: intentionally returning 500")
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	addr := "127.0.0.1:" + port
	log.Printf("demoapp listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
