package stream

import (
	"encoding/json"
	"fmt"
	"net/http"
)

const DoneSentinel = "[DONE]"

func Prepare(w http.ResponseWriter) bool {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	return true
}

func WriteJSON(w http.ResponseWriter, eventName string, payload any) error {
	flusher := w.(http.Flusher)
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", eventName); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func WriteDone(w http.ResponseWriter) error {
	flusher := w.(http.Flusher)
	if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", DoneSentinel); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
