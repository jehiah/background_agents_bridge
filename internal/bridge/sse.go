package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// sseEvent is a decoded Server-Sent Event from OpenCode. Properties is left
// generic because its shape varies by event type; the streaming dispatcher
// navigates it with the gstr/gmap helpers.
type sseEvent struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

// parseSSE reads the OpenCode event stream from r. onChunk is called after every
// successful read (used to reset the inactivity deadline). handle is invoked for
// each decoded event; returning stop=true ends parsing cleanly, and a non-nil
// error aborts it. Events are separated by blank lines ("\n\n").
func parseSSE(r io.Reader, onChunk func(), handle func(sseEvent) (stop bool, err error)) error {
	br := bufio.NewReader(r)
	var buf []byte
	tmp := make([]byte, 16*1024)
	sep := []byte("\n\n")

	for {
		n, err := br.Read(tmp)
		if n > 0 {
			if onChunk != nil {
				onChunk()
			}
			buf = append(buf, tmp[:n]...)
			for {
				i := bytes.Index(buf, sep)
				if i < 0 {
					break
				}
				ev, ok := parseSSEBlock(buf[:i])
				// Compact: move the unconsumed tail to the front (copy handles
				// the overlap safely).
				buf = append(buf[:0], buf[i+2:]...)
				if ok {
					stop, herr := handle(ev)
					if herr != nil {
						return herr
					}
					if stop {
						return nil
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// parseSSEBlock extracts and decodes the JSON payload from one SSE block,
// concatenating multiple "data:" lines with newlines (matching the Python
// parser). It returns ok=false when there is no data or the JSON is invalid.
func parseSSEBlock(block []byte) (sseEvent, bool) {
	var data []byte
	for _, line := range bytes.Split(block, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		content := bytes.TrimLeft(line[len("data:"):], " \t")
		if len(content) == 0 {
			continue
		}
		if len(data) > 0 {
			data = append(data, '\n')
		}
		data = append(data, content...)
	}
	if len(data) == 0 {
		return sseEvent{}, false
	}
	var ev sseEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return sseEvent{}, false
	}
	return ev, true
}
