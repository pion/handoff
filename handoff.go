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
	"log"
	"net/http"
	"sync"

	"github.com/pion/webrtc/v4"
)

//go:embed handoff.js
var handoffJavaScript []byte

func JavaScript() string {
	return string(handoffJavaScript)
}

type controlMessage struct {
	Kind             string          `json:"kind"`
	RequestID        string          `json:"requestId,omitempty"`
	PeerConnectionID string          `json:"peerConnectionId,omitempty"`
	Event            string          `json:"event,omitempty"`
	Name             string          `json:"name,omitempty"`
	DataChannelID    string          `json:"dataChannelId,omitempty"`
	Data             json.RawMessage `json:"data,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	Error            string          `json:"error,omitempty"`
}

type Server struct {
	mu       sync.Mutex
	sessions map[*controlSession]struct{}
	logger   *log.Logger

	OnPeerConnection     func(*webrtc.PeerConnection)
	OnDataChannel        func(*webrtc.DataChannel)
	OnDataChannelMessage func(webrtc.DataChannelMessage) bool
}

type controlSession struct {
	server *Server
	pc     *webrtc.PeerConnection

	mu        sync.Mutex
	closeOnce sync.Once

	dc    *webrtc.DataChannel
	peers map[string]*managedPeer
}

type managedPeer struct {
	id      string
	session *controlSession
	pc      *webrtc.PeerConnection
	dcs     map[string]*webrtc.DataChannel

	closeOnce sync.Once
}

type Option func(*Server)

func WithMessageLogger(logger *log.Logger) Option {
	if logger == nil {
		logger = log.Default()
	}
	return func(server *Server) {
		server.logger = logger
	}
}

func NewServer(options ...Option) *Server {
	server := &Server{sessions: map[*controlSession]struct{}{}}
	for _, option := range options {
		option(server)
	}
	return server
}

func (server *Server) SetupHandlers(mux *http.ServeMux) {
	if mux == nil {
		mux = http.DefaultServeMux
	}
	mux.HandleFunc("/handoff", server.handleBootstrap)
	mux.HandleFunc("/handoff.js", func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		if _, err := responseWriter.Write(handoffJavaScript); err != nil {
			fmt.Printf("Failed to write handoff.js: %v\n", err)
		}
	})
}

func (server *Server) handleBootstrap(responseWriter http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer func() {
		_ = r.Body.Close()
	}()
	var request struct {
		Offer webrtc.SessionDescription `json:"offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(responseWriter, fmt.Sprintf("invalid bootstrap payload: %v", err), http.StatusBadRequest)
		return
	}
	server.logMessage("handoff <- bootstrap", request)
	answer, err := server.newControlSession(request.Offer)
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusInternalServerError)
		return
	}
	response := struct {
		Answer webrtc.SessionDescription `json:"answer"`
	}{Answer: answer}
	server.logMessage("handoff -> bootstrap", response)
	responseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err = json.NewEncoder(responseWriter).Encode(response); err != nil {
		fmt.Printf("Failed to write handoff bootstrap response: %v\n", err)
	}
}

func (server *Server) newControlSession(offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("create control peer connection: %w", err)
	}
	session := &controlSession{
		server: server,
		pc:     pc,
		peers:  map[string]*managedPeer{},
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if terminalState(state) {
			session.close()
		}
	})
	pc.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		if dataChannel.Label() == "handoff-control" {
			session.attachControlChannel(dataChannel)
		}
	})
	if err = pc.SetRemoteDescription(offer); err != nil {
		session.close()
		return webrtc.SessionDescription{}, fmt.Errorf("set control remote description: %w", err)
	}
	answer, err := localDescription(pc, func() (webrtc.SessionDescription, error) {
		return pc.CreateAnswer(nil)
	})
	if err != nil {
		session.close()
		return webrtc.SessionDescription{}, fmt.Errorf("create control answer: %w", err)
	}
	server.mu.Lock()
	server.sessions[session] = struct{}{}
	server.mu.Unlock()
	return answer, nil
}

func (session *controlSession) attachControlChannel(dataChannel *webrtc.DataChannel) {
	session.mu.Lock()
	if session.dc != nil {
		session.mu.Unlock()
		return
	}
	session.dc = dataChannel
	session.mu.Unlock()
	dataChannel.OnClose(func() {
		session.close()
	})
	dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		var request controlMessage
		if err := json.Unmarshal(message.Data, &request); err != nil {
			fmt.Printf("Failed to decode control message: %v\n", err)
			return
		}
		if request.Kind != "request" {
			return
		}
		session.server.logMessage("handoff <- control", request)

		response := controlMessage{Kind: "response", RequestID: request.RequestID}
		result, err := session.dispatch(request)
		switch {
		case err != nil:
			response.Error = err.Error()
		case result != nil:
			if response.Result, err = json.Marshal(result); err != nil {
				response.Error = fmt.Sprintf("failed to encode response: %v", err)
			}
		}
		session.server.logMessage("handoff -> control", response)

		if err = session.send(response); err != nil {
			fmt.Printf("Failed to send control response: %v\n", err)
		}
	})
}

func (session *controlSession) dispatch(request controlMessage) (any, error) {
	args, err := decodeArgs(request.Data)
	if err != nil {
		return nil, fmt.Errorf("decode handoff arguments: %w", err)
	}
	if request.Event == "new" {
		return session.newPeer(args)
	}
	if request.PeerConnectionID == "" {
		return nil, errors.New("missing peer connection id")
	}
	session.mu.Lock()
	peer := session.peers[request.PeerConnectionID]
	session.mu.Unlock()
	if peer == nil {
		return nil, fmt.Errorf("unknown peer connection %q", request.PeerConnectionID)
	}
	switch request.Event {
	case "createDataChannel":
		return peer.createDataChannel(args)
	case "createOffer":
		return peer.createOffer()
	case "setLocalDescription":
		return nil, peer.setLocalDescription(args)
	case "setRemoteDescription":
		return nil, peer.setRemoteDescription(args)
	case "createAnswer":
		return peer.createAnswer()
	case "addIceCandidate":
		return nil, peer.addICECandidate(args)
	case "sendDataChannelMessage":
		return nil, peer.sendDataChannelMessage(args)
	default:
		return nil, fmt.Errorf("unsupported handoff event %q", request.Event)
	}
}

func (session *controlSession) newPeer(args []json.RawMessage) (string, error) {
	configuration := webrtc.Configuration{}
	if len(args) > 0 && string(args[0]) != "null" {
		if err := json.Unmarshal(args[0], &configuration); err != nil {
			return "", err
		}
	}
	id, err := newHandoffID()
	if err != nil {
		return "", err
	}
	pc, err := webrtc.NewPeerConnection(configuration)
	if err != nil {
		return "", err
	}
	if session.server.OnPeerConnection != nil {
		session.server.OnPeerConnection(pc)
	}
	peer := &managedPeer{id: id, session: session, pc: pc, dcs: map[string]*webrtc.DataChannel{}}
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		session.event(id, "connectionstatechange", "", map[string]string{
			"connectionState": state.String(),
		})
		if terminalState(state) {
			peer.close()
		}
	})
	pc.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		dataChannelID, err := peer.registerDataChannel(dataChannel)
		if err != nil {
			fmt.Printf("Failed to register data channel: %v\n", err)
			return
		}
		session.event(id, "datachannel", dataChannelID, map[string]string{
			"label": dataChannel.Label(),
		})
	})
	session.mu.Lock()
	session.peers[id] = peer
	session.mu.Unlock()
	return id, nil
}

func (session *controlSession) event(peerConnectionID, name, dataChannelID string, data any) {
	message := controlMessage{
		Kind:             "event",
		PeerConnectionID: peerConnectionID,
		Name:             name,
		DataChannelID:    dataChannelID,
	}
	if data != nil {
		eventJSON, err := json.Marshal(data)
		if err != nil {
			fmt.Printf("Failed to encode handoff event payload: %v\n", err)
			return
		}
		message.Data = eventJSON
	}
	if err := session.send(message); err != nil {
		fmt.Printf("Failed to send handoff event: %v\n", err)
	}
}

func (session *controlSession) send(message controlMessage) error {
	messageJSON, err := json.Marshal(message)
	if err != nil {
		return err
	}
	session.mu.Lock()
	dataChannel := session.dc
	if dataChannel == nil {
		session.mu.Unlock()
		return errors.New("missing control channel")
	}
	defer session.mu.Unlock()
	return dataChannel.SendText(string(messageJSON))
}

func (server *Server) logMessage(prefix string, message any) {
	if server.logger == nil {
		return
	}
	messageJSON, err := json.Marshal(message)
	if err != nil {
		server.logger.Printf("%s: %v", prefix, err)
		return
	}
	server.logger.Printf("%s %s", prefix, messageJSON)
}

func (session *controlSession) close() {
	session.closeOnce.Do(func() {
		session.server.mu.Lock()
		delete(session.server.sessions, session)
		session.server.mu.Unlock()

		session.mu.Lock()
		peers := make([]*managedPeer, 0, len(session.peers))
		for _, peer := range session.peers {
			peers = append(peers, peer)
		}
		session.peers = map[string]*managedPeer{}
		dataChannel := session.dc
		session.dc = nil
		session.mu.Unlock()
		for _, peer := range peers {
			peer.close()
		}
		if dataChannel != nil {
			_ = dataChannel.Close()
		}
		_ = session.pc.Close()
	})
}

func (peer *managedPeer) close() {
	peer.closeOnce.Do(func() {
		peer.session.mu.Lock()
		delete(peer.session.peers, peer.id)
		peer.session.mu.Unlock()

		_ = peer.pc.Close()
	})
}

func (peer *managedPeer) createDataChannel(args []json.RawMessage) (string, error) {
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
	dataChannel, err := peer.pc.CreateDataChannel(label, options)
	if err != nil {
		return "", err
	}
	return peer.registerDataChannel(dataChannel)
}

func (peer *managedPeer) createOffer() (webrtc.SessionDescription, error) {
	return localDescription(peer.pc, func() (webrtc.SessionDescription, error) {
		return peer.pc.CreateOffer(nil)
	})
}

func (peer *managedPeer) setLocalDescription(args []json.RawMessage) error {
	if peer.pc.LocalDescription() != nil {
		return nil
	}
	description, err := decodeSessionDescriptionArgument(args)
	if err != nil {
		return err
	}
	return peer.pc.SetLocalDescription(description)
}

func (peer *managedPeer) setRemoteDescription(args []json.RawMessage) error {
	description, err := decodeSessionDescriptionArgument(args)
	if err != nil {
		return err
	}
	return peer.pc.SetRemoteDescription(description)
}

func (peer *managedPeer) createAnswer() (webrtc.SessionDescription, error) {
	return localDescription(peer.pc, func() (webrtc.SessionDescription, error) {
		return peer.pc.CreateAnswer(nil)
	})
}

func (peer *managedPeer) addICECandidate(args []json.RawMessage) error {
	if len(args) == 0 || string(args[0]) == "null" {
		return nil
	}
	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal(args[0], &candidate); err != nil {
		return err
	}
	return peer.pc.AddICECandidate(candidate)
}

func (peer *managedPeer) sendDataChannelMessage(args []json.RawMessage) error {
	if len(args) < 2 {
		return errors.New("missing data channel message arguments")
	}
	var dataChannelID string
	if err := json.Unmarshal(args[0], &dataChannelID); err != nil {
		return err
	}
	var message string
	if err := json.Unmarshal(args[1], &message); err != nil {
		return err
	}
	peer.session.mu.Lock()
	dataChannel := peer.dcs[dataChannelID]
	peer.session.mu.Unlock()
	if dataChannel == nil {
		return fmt.Errorf("unknown data channel %q", dataChannelID)
	}
	return dataChannel.SendText(message)
}

func (peer *managedPeer) registerDataChannel(dataChannel *webrtc.DataChannel) (string, error) {
	dataChannelID, err := newHandoffID()
	if err != nil {
		return "", err
	}
	peer.session.mu.Lock()
	peer.dcs[dataChannelID] = dataChannel
	peer.session.mu.Unlock()
	if peer.session.server.OnDataChannel != nil {
		peer.session.server.OnDataChannel(dataChannel)
	}
	dataChannel.OnOpen(func() {
		peer.session.event(peer.id, "datachannelopen", dataChannelID, nil)
	})
	dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		if peer.session.server.OnDataChannelMessage != nil &&
			!peer.session.server.OnDataChannelMessage(message) {
			return
		}
		peer.session.event(peer.id, "datachannelmessage", dataChannelID, map[string]string{
			"data": string(message.Data),
		})
	})

	return dataChannelID, nil
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

func localDescription(
	peerConnection *webrtc.PeerConnection,
	create func() (webrtc.SessionDescription, error),
) (webrtc.SessionDescription, error) {
	description, err := create()
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	if err = peerConnection.SetLocalDescription(description); err != nil {
		return webrtc.SessionDescription{}, err
	}
	<-gatherComplete
	return *peerConnection.LocalDescription(), nil
}

func terminalState(state webrtc.PeerConnectionState) bool {
	return state == webrtc.PeerConnectionStateClosed ||
		state == webrtc.PeerConnectionStateDisconnected ||
		state == webrtc.PeerConnectionStateFailed
}

func newHandoffID() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(randomBytes), nil
}
