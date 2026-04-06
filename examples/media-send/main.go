// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package main

import (
	"fmt"
	"net"
	"net/http"

	"github.com/pion/handoff"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func main() {
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		"video",
		"handoff",
	)
	if err != nil {
		panic(err)
	}

	packetConn, err := net.ListenPacket("udp4", ":5004")
	if err != nil {
		panic(err)
	}

	go func() {
		buffer := make([]byte, 1600)
		for {
			n, _, readErr := packetConn.ReadFrom(buffer)
			if readErr != nil {
				return
			}

			var packet rtp.Packet
			if unmarshalErr := packet.Unmarshal(buffer[:n]); unmarshalErr != nil {
				fmt.Printf("Failed to parse RTP packet: %v\n", unmarshalErr)
				continue
			}
			if writeErr := videoTrack.WriteRTP(&packet); writeErr != nil {
				fmt.Printf("Failed to forward RTP packet: %v\n", writeErr)
			}
		}
	}()

	mux := http.NewServeMux()
	server := handoff.NewServer()
	server.OnPeerConnection = func(peerConnection *webrtc.PeerConnection) {
		rtpSender, addTrackErr := peerConnection.AddTrack(videoTrack)
		if addTrackErr != nil {
			panic(addTrackErr)
		}

		go func() {
			rtcpBuffer := make([]byte, 1500)
			for {
				if _, _, readErr := rtpSender.Read(rtcpBuffer); readErr != nil {
					return
				}
			}
		}()
	}
	server.SetupHandlers(mux)
	mux.HandleFunc("/", func(responseWriter http.ResponseWriter, r *http.Request) {
		http.ServeFile(responseWriter, r, "./index.html")
	})

	fmt.Println("Serving http://localhost:8080")
	fmt.Println("Send VP8 RTP to udp://127.0.0.1:5004")
	//nolint:gosec
	if err = http.ListenAndServe(":8080", mux); err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
	}
}
