package main

import (
	_ "embed"
	"fmt"
	"log"
	"net/http"
)

//go:embed frontend/dist/index.html
var browserSourceHtml []byte

func runBrowserSource(port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			w.Write(browserSourceHtml)
		} else {
			http.NotFound(w, r)
		}
	})

	log.Printf("Browser Source address: http://127.0.0.1:%d/app\n", port)

	err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", port), mux)
	if err != nil {
		log.Fatalf("Failed to start Browser Source server: %v", err)
	}
}
