// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pion/handoff"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"
)

func main() {
	mux := http.NewServeMux()
	server := handoff.NewServer()
	server.OnPeerConnection = func(peerConnection *webrtc.PeerConnection) {
		loopbackTrack, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
			"video",
			"handoff",
		)
		if err != nil {
			panic(err)
		}

		rtpSender, err := peerConnection.AddTrack(loopbackTrack)
		if err != nil {
			panic(err)
		}

		go func() {
			rtcpBuffer := make([]byte, 1500)
			for {
				if _, _, err = rtpSender.Read(rtcpBuffer); err != nil {
					return
				}
			}
		}()

		peerConnection.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			if track.Kind() != webrtc.RTPCodecTypeVideo {
				return
			}
			if !strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeVP8) {
				fmt.Printf("Skipping unsupported video codec: %s\n", track.Codec().MimeType)
				return
			}

			fileName := fmt.Sprintf("output-%s.ivf", time.Now().Format("20060102-150405"))
			writer, err := ivfwriter.New(fileName, ivfwriter.WithCodec(webrtc.MimeTypeVP8))
			if err != nil {
				fmt.Printf("Failed to create %s: %v\n", fileName, err)
				return
			}

			fmt.Printf("Saving incoming video to %s\n", fileName)
			defer func() {
				_ = writer.Close()
			}()

			for {
				rtpPacket, _, err := track.ReadRTP()
				if err != nil {
					if err != io.EOF {
						fmt.Printf("Video track ended: %v\n", err)
					}
					return
				}
				if err = writer.WriteRTP(rtpPacket); err != nil {
					fmt.Printf("Failed to write RTP to %s: %v\n", fileName, err)
					return
				}
				if err = loopbackTrack.WriteRTP(rtpPacket); err != nil {
					if err != io.ErrClosedPipe {
						fmt.Printf("Failed to loop video back to browser: %v\n", err)
					}
					return
				}
			}
		})
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
