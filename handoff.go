// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

// Package handoff proxies RTCPeerConnection operations from the browser to
// backend-owned Pion peer connections.
package handoff

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

//go:embed handoff.js
var handoffJavaScript []byte

type handoffPayload struct {
	ID    string          `json:"id,omitempty"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type handoffEvent struct {
	Name          string          `json:"name"`
	DataChannelID string          `json:"dataChannelID,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
}

type Server struct {
	mu              sync.RWMutex
	peerConnections map[string]*managedPeerConnection
}

type managedPeerConnection struct {
	peerConnection *webrtc.PeerConnection

	mu         sync.Mutex
	eventQueue []handoffEvent
}

func NewServer() *Server {
	return &Server{
		peerConnections: map[string]*managedPeerConnection{},
	}
}

func (server *Server) SetupHandlers(mux *http.ServeMux) {
	if mux == nil {
		mux = http.DefaultServeMux
	}

	mux.HandleFunc("/handoff", server.handleRPC)
	mux.HandleFunc("/handoff/events", server.handleEvents)
	mux.HandleFunc("/handoff.js", func(responseWriter http.ResponseWriter, r *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		if _, err := responseWriter.Write(handoffJavaScript); err != nil {
			fmt.Printf("Failed to write handoff.js: %v\n", err)
		}
	})
}

func (server *Server) handleRPC(responseWriter http.ResponseWriter, r *http.Request) {
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

	responseBody, statusCode, err := server.dispatch(payload)
	if err != nil {
		http.Error(responseWriter, err.Error(), statusCode)

		return
	}
	if payload.Event == "new" {
		payload.ID = responseBody
	}
	if responseBody == "" {
		responseWriter.WriteHeader(http.StatusNoContent)

		return
	}

	responseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err = responseWriter.Write([]byte(responseBody)); err != nil {
		fmt.Printf("Failed to write handoff response: %v\n", err)
	}
}

func (server *Server) handleEvents(responseWriter http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	peerConnectionID := r.URL.Query().Get("id")
	if peerConnectionID == "" {
		http.Error(responseWriter, "missing peer connection id", http.StatusBadRequest)

		return
	}

	managedPeerConnection, err := server.getPeerConnection(peerConnectionID)
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusNotFound)

		return
	}

	events := managedPeerConnection.drainEvents()
	if len(events) == 0 {
		responseWriter.WriteHeader(http.StatusNoContent)

		return
	}

	responseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for index, event := range events {
		eventJSON, err := json.Marshal(event)
		if err != nil {
			http.Error(responseWriter, fmt.Sprintf("failed to encode event: %v", err), http.StatusInternalServerError)

			return
		}

		if index > 0 {
			if _, err = responseWriter.Write([]byte("\n")); err != nil {
				fmt.Printf("Failed to write handoff event separator: %v\n", err)

				return
			}
		}

		if _, err = responseWriter.Write(eventJSON); err != nil {
			fmt.Printf("Failed to write handoff event: %v\n", err)

			return
		}
	}
}

func (server *Server) dispatch(payload handoffPayload) (string, int, error) {
	args, err := decodeArgs(payload.Data)
	if err != nil {
		return "", http.StatusBadRequest, fmt.Errorf("failed to decode handoff arguments: %w", err)
	}

	if payload.Event == "new" {
		id, err := server.newPeerConnection(args)
		if err != nil {
			return "", http.StatusInternalServerError, fmt.Errorf("failed to create peer connection: %w", err)
		}

		payload.ID = id

		return id, http.StatusOK, nil
	}

	managedPeerConnection, err := server.getPeerConnection(payload.ID)
	if err != nil {
		return "", http.StatusNotFound, err
	}

	switch payload.Event {
	case "createDataChannel":
		dataChannelID, err := managedPeerConnection.createDataChannel(args)
		if err != nil {
			return "", http.StatusBadRequest, fmt.Errorf("failed to create data channel: %w", err)
		}

		return dataChannelID, http.StatusOK, nil
	case "createOffer":
		offer, err := managedPeerConnection.createOffer()
		if err != nil {
			return "", http.StatusInternalServerError, fmt.Errorf("failed to create offer: %w", err)
		}

		return offer, http.StatusOK, nil
	case "setLocalDescription":
		if err = managedPeerConnection.setLocalDescription(args); err != nil {
			return "", http.StatusBadRequest, fmt.Errorf("failed to set local description: %w", err)
		}

		return "", http.StatusNoContent, nil
	case "setRemoteDescription":
		if err = managedPeerConnection.setRemoteDescription(args); err != nil {
			return "", http.StatusBadRequest, fmt.Errorf("failed to set remote description: %w", err)
		}

		return "", http.StatusNoContent, nil
	case "createAnswer":
		answer, err := managedPeerConnection.createAnswer()
		if err != nil {
			return "", http.StatusInternalServerError, fmt.Errorf("failed to create answer: %w", err)
		}

		return answer, http.StatusOK, nil
	case "addIceCandidate":
		if err = managedPeerConnection.addICECandidate(args); err != nil {
			return "", http.StatusBadRequest, fmt.Errorf("failed to add ICE candidate: %w", err)
		}

		return "", http.StatusNoContent, nil
	default:
		return "", http.StatusBadRequest, fmt.Errorf("unsupported handoff event %q", payload.Event)
	}
}

func (server *Server) newPeerConnection(args []json.RawMessage) (string, error) {
	configuration, err := parsePeerConnectionConfiguration(args)
	if err != nil {
		return "", err
	}

	peerConnection, err := webrtc.NewPeerConnection(configuration)
	if err != nil {
		return "", err
	}

	managedPeerConnection := &managedPeerConnection{
		peerConnection: peerConnection,
	}

	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		managedPeerConnection.enqueueEvent("connectionstatechange", "", map[string]string{
			"connectionState": state.String(),
		})
	})

	peerConnection.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		dataChannelID := managedPeerConnection.registerDataChannel(dataChannel)
		managedPeerConnection.enqueueEvent("datachannel", dataChannelID, map[string]string{
			"label": dataChannel.Label(),
		})
	})

	id, err := newHandoffID()
	if err != nil {
		return "", err
	}

	server.mu.Lock()
	server.peerConnections[id] = managedPeerConnection
	server.mu.Unlock()

	return id, nil
}

func (server *Server) getPeerConnection(id string) (*managedPeerConnection, error) {
	if id == "" {
		return nil, errors.New("missing peer connection id")
	}

	server.mu.RLock()
	managedPeerConnection, ok := server.peerConnections[id]
	server.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown peer connection %q", id)
	}

	return managedPeerConnection, nil
}

func (managedPeerConnection *managedPeerConnection) createDataChannel(args []json.RawMessage) (string, error) {
	var label string
	if len(args) > 0 {
		if err := json.Unmarshal(args[0], &label); err != nil {
			return "", err
		}
	}

	var options *webrtc.DataChannelInit
	if len(args) > 1 && string(args[1]) != "null" {
		options = &webrtc.DataChannelInit{}
		if err := json.Unmarshal(args[1], options); err != nil {
			return "", err
		}
	}

	dataChannel, err := managedPeerConnection.peerConnection.CreateDataChannel(label, options)
	if err != nil {
		return "", err
	}

	return managedPeerConnection.registerDataChannel(dataChannel), nil
}

func (managedPeerConnection *managedPeerConnection) createOffer() (string, error) {
	offer, err := managedPeerConnection.peerConnection.CreateOffer(nil)
	if err != nil {
		return "", err
	}

	gatherComplete := webrtc.GatheringCompletePromise(managedPeerConnection.peerConnection)
	if err = managedPeerConnection.peerConnection.SetLocalDescription(offer); err != nil {
		return "", err
	}

	<-gatherComplete

	response, err := json.Marshal(managedPeerConnection.peerConnection.LocalDescription())
	if err != nil {
		return "", err
	}

	return string(response), nil
}

func (managedPeerConnection *managedPeerConnection) setLocalDescription(args []json.RawMessage) error {
	if managedPeerConnection.peerConnection.LocalDescription() != nil {
		return nil
	}

	description, err := decodeSessionDescriptionArgument(args)
	if err != nil {
		return err
	}

	return managedPeerConnection.peerConnection.SetLocalDescription(description)
}

func (managedPeerConnection *managedPeerConnection) setRemoteDescription(args []json.RawMessage) error {
	description, err := decodeSessionDescriptionArgument(args)
	if err != nil {
		return err
	}

	return managedPeerConnection.peerConnection.SetRemoteDescription(description)
}

func (managedPeerConnection *managedPeerConnection) createAnswer() (string, error) {
	answer, err := managedPeerConnection.peerConnection.CreateAnswer(nil)
	if err != nil {
		return "", err
	}

	gatherComplete := webrtc.GatheringCompletePromise(managedPeerConnection.peerConnection)
	if err = managedPeerConnection.peerConnection.SetLocalDescription(answer); err != nil {
		return "", err
	}

	<-gatherComplete

	response, err := json.Marshal(managedPeerConnection.peerConnection.LocalDescription())
	if err != nil {
		return "", err
	}

	return string(response), nil
}

func (managedPeerConnection *managedPeerConnection) addICECandidate(args []json.RawMessage) error {
	if len(args) == 0 || string(args[0]) == "null" {
		return nil
	}

	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal(args[0], &candidate); err != nil {
		return err
	}

	return managedPeerConnection.peerConnection.AddICECandidate(candidate)
}

func (managedPeerConnection *managedPeerConnection) registerDataChannel(dataChannel *webrtc.DataChannel) string {
	dataChannelID, err := newHandoffID()
	if err != nil {
		panic(err)
	}

	dataChannel.OnOpen(func() {
		managedPeerConnection.enqueueEvent("datachannelopen", dataChannelID, nil)
	})

	dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		fmt.Printf("%s - %s\n", time.Now().Format("3:04:05 PM"), string(message.Data))
		managedPeerConnection.enqueueEvent("datachannelmessage", dataChannelID, map[string]string{
			"data": string(message.Data),
		})
	})

	return dataChannelID
}

func (managedPeerConnection *managedPeerConnection) enqueueEvent(name, dataChannelID string, data any) {
	event := handoffEvent{
		Name:          name,
		DataChannelID: dataChannelID,
	}
	if data != nil {
		eventJSON, err := json.Marshal(data)
		if err != nil {
			fmt.Printf("Failed to encode handoff event payload: %v\n", err)

			return
		}

		event.Data = eventJSON
	}

	managedPeerConnection.mu.Lock()
	managedPeerConnection.eventQueue = append(managedPeerConnection.eventQueue, event)
	managedPeerConnection.mu.Unlock()
}

func (managedPeerConnection *managedPeerConnection) drainEvents() []handoffEvent {
	managedPeerConnection.mu.Lock()
	defer managedPeerConnection.mu.Unlock()

	events := append([]handoffEvent(nil), managedPeerConnection.eventQueue...)
	managedPeerConnection.eventQueue = managedPeerConnection.eventQueue[:0]

	return events
}

func parsePeerConnectionConfiguration(args []json.RawMessage) (webrtc.Configuration, error) {
	if len(args) == 0 || string(args[0]) == "null" {
		return webrtc.Configuration{}, nil
	}

	var configuration webrtc.Configuration
	if err := json.Unmarshal(args[0], &configuration); err != nil {
		return webrtc.Configuration{}, err
	}

	return configuration, nil
}

func decodeSessionDescriptionArgument(args []json.RawMessage) (webrtc.SessionDescription, error) {
	if len(args) == 0 {
		return webrtc.SessionDescription{}, errors.New("missing session description")
	}

	var description webrtc.SessionDescription
	if err := json.Unmarshal(args[0], &description); err != nil {
		return webrtc.SessionDescription{}, err
	}

	return description, nil
}

func decodeArgs(data json.RawMessage) ([]json.RawMessage, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}

	var args []json.RawMessage
	if err := json.Unmarshal(data, &args); err != nil {
		return nil, err
	}

	return args, nil
}

func newHandoffID() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	return hex.EncodeToString(randomBytes), nil
}
