package main

import (
	_ "embed"
	"fmt"
	"log"
	"net"
	"net/http"
)

//go:embed frontend/dist/index.html
var browserSourceHtml []byte

// startBrowserSource binds the Browser Source port and serves it in the
// background. Errors after startup are sent to errCh.
func startBrowserSource(port int, errCh chan<- error) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			w.Write(browserSourceHtml)
		} else {
			http.NotFound(w, r)
		}
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("failed to start Browser Source server: %w", err)
	}

	log.Printf("Browser Source address: http://127.0.0.1:%d/app\n", port)

	go func() {
		if err := http.Serve(ln, mux); err != nil {
			errCh <- fmt.Errorf("Browser Source server: %w", err)
		}
	}()
	return nil
}
