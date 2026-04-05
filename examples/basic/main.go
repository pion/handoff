// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

// basic demonstrates serving a static HTML page over HTTP.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
)

type handoffPayload struct {
	ID    string          `json:"id,omitempty"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func main() {
	setupEventHandler()
	setupStaticHandler()

	fmt.Println("Serving http://localhost:8080")
	//nolint:gosec
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
	}
}

func setupEventHandler() {
	http.HandleFunc("/handoff", func(responseWriter http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		defer func() {
			_ = r.Body.Close()
		}()

		var payload handoffPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(responseWriter, fmt.Sprintf("invalid event payload: %v", err), http.StatusBadRequest)

			return
		}

		fmt.Printf("%s %s %s\n", payload.ID, payload.Event, payload.Data)

		if payload.Event == "new" {
			id, err := newHandoffID()
			if err != nil {
				http.Error(responseWriter, fmt.Sprintf("failed to generate handoff id: %v", err), http.StatusInternalServerError)

				return
			}

			payload.ID = id
			responseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")

			if _, err := responseWriter.Write([]byte(payload.ID)); err != nil {
				fmt.Printf("Failed to write handoff response: %v\n", err)
			}
			return
		}

		responseWriter.WriteHeader(http.StatusNoContent)
	})
}

func newHandoffID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(bytes[:]), nil
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
