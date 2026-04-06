// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

// datachannel demonstrates serving a static HTML page over HTTP.
package main

import (
	"fmt"
	"net/http"

	"github.com/pion/handoff"
)

func main() {
	mux := http.NewServeMux()
	handoff.NewServer().SetupHandlers(mux)
	mux.HandleFunc("/", func(responseWriter http.ResponseWriter, r *http.Request) {
		http.ServeFile(responseWriter, r, "./index.html")
	})

	fmt.Println("Serving http://localhost:8080")
	//nolint:gosec
	if err := http.ListenAndServe(":8080", mux); err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
	}
}
