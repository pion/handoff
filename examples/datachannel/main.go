// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

// datachannel demonstrates serving a static HTML page over HTTP.
package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/pion/handoff"
	"github.com/pion/webrtc/v4"
)

func main() {
	mux := http.NewServeMux()
	server := handoff.NewServer()
	server.OnPeerConnection = func(*webrtc.PeerConnection) {
		fmt.Println("PeerConnection created on backend")
	}
	server.OnDataChannel = func(dataChannel *webrtc.DataChannel) {
		fmt.Printf("DataChannel created on backend: %s\n", dataChannel.Label())
	}
	server.OnDataChannelMessage = func(message webrtc.DataChannelMessage) {
		fmt.Printf("%s - %s\n", time.Now().Format("3:04:05 PM"), string(message.Data))
	}
	server.SetupHandlers(mux)
	mux.HandleFunc("/", func(responseWriter http.ResponseWriter, r *http.Request) {
		http.ServeFile(responseWriter, r, "./index.html")
	})

	fmt.Println("Serving http://localhost:8080")
	//nolint:gosec
	if err := http.ListenAndServe(":8080", mux); err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
	}
}
