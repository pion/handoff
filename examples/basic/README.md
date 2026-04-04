# basic
basic demonstrates serving a browser page from a Go HTTP server that creates two
local RTCPeerConnections and exchanges DataChannel messages between them.

## Instructions

### Run basic
Execute `go run main.go`

### Open the Web UI
Open [http://localhost:8080](http://localhost:8080). The page creates an
`offerer` and `answerer` in the browser, connects them over a DataChannel, and
has both sides send the current time every second after you click `Start`.
