// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

// basic demonstrates serving a static HTML page over HTTP.
package main

import (
	"fmt"
	"net/http"
)

func main() {
	setupStaticHandler()

	fmt.Println("Serving http://localhost:8080")
	//nolint:gosec
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
	}
}

func setupStaticHandler() {
	http.HandleFunc("/", func(responseWriter http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(responseWriter, r)

			return
		}

		http.ServeFile(responseWriter, r, "./index.html")
	})
}
