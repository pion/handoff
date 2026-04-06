# media-send
media-send demonstrates a browser video call where the page still captures
camera with `getUserMedia`, but the answerer can move to the backend with
`handoff`.

When `Override` is disabled:
- both peer connections stay in the browser
- the remote video is just the local camera flowing through the browser-only example

When `Override` is enabled:
- the answerer runs in the backend
- the backend listens for VP8 RTP on `udp://127.0.0.1:5004`
- the backend forwards that RTP into the WebRTC session so the browser shows it
- the page still uses normal browser capture, but the backend replaces the
  remote video path

## Instructions

### Run media-send
Execute `go run .`

### Open the Web UI
Open [http://localhost:8080](http://localhost:8080), enable `Override`, click
`Start`, and allow camera access.

### Send test video with FFmpeg
Execute:

```sh
ffmpeg -re -f lavfi -i testsrc=size=1280x720:rate=30 -an -c:v libvpx -deadline realtime -cpu-used 8 -g 30 -f rtp rtp://127.0.0.1:5004
```

Any VP8 RTP sent to `udp://127.0.0.1:5004` will be forwarded to the browser.
This only applies when `Override` is enabled.
