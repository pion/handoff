# media-save
media-save demonstrates a browser video call where the answerer can stay local
or move to the backend with `handoff`.

When `Override` is enabled:
- the answerer runs in the backend
- the backend saves the incoming VP8 video to `output-*.ivf`
- the backend loops the same RTP back to the browser, so the page still shows remote video

## Instructions

### Run media-save
Execute `go run .`

### Open the Web UI
Open [http://localhost:8080](http://localhost:8080), click `Start`, and allow
camera access. Enable `Override` first if you want the backend to save the
video to disk.
