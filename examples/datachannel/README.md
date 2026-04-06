# basic
datachannel demonstrates how handoff can move a RTCPeerConnection
to a backend Go process. If you press `start` both PeerConnections will
run in the browser. If you check `Override` handoff is enabled and will
move the WebRTC session out of the browser.

You should only see messages in the CLI when `Override` is enabled. See
`index.html` for handoff usage.

## Instructions

### Run basic
Execute `go run .`

### Open the Web UI
Open [http://localhost:8080](http://localhost:8080). Click `Start` to run the
example with local browser peer connections, or enable `Override` first to
redirect the answerer API to the backend while leaving the offerer in the
browser.
