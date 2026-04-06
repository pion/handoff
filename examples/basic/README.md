# basic
basic demonstrates serving a browser page from a Go HTTP server. Click `Start`
to run two local browser RTCPeerConnections, or enable `Override` first to
redirect the answerer API to the backend while the offerer stays in the browser
and sends
DataChannel messages. The example mounts the root `github.com/pion/handoff`
package on its own `http.ServeMux`, and `handoff.SetupHandlers` serves the root
`handoff.js` browser library for the override path. That library establishes one
WebRTC control session per page/tab for the overridden API calls.

## Instructions

### Run basic
Execute `go run .`

### Open the Web UI
Open [http://localhost:8080](http://localhost:8080). Click `Start` to run the
example with local browser peer connections, or enable `Override` first to
redirect the answerer API to the backend while leaving the offerer in the
browser.
