package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type sseEvent struct {
	Event string
	Data  string
}

type sseHub struct {
	mu      sync.Mutex
	clients map[chan sseEvent]struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{
		clients: make(map[chan sseEvent]struct{}),
	}
}

func (h *sseHub) Publish(eventType string, payload any) {
	if h == nil || eventType == "" {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	evt := sseEvent{Event: eventType, Data: string(raw)}
	h.mu.Lock()
	for ch := range h.clients {
		select {
		case ch <- evt:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *sseHub) subscribe() (chan sseEvent, func()) {
	ch := make(chan sseEvent, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	unsubscribe := func() {
		h.mu.Lock()
		if _, ok := h.clients[ch]; ok {
			delete(h.clients, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	return ch, unsubscribe
}

func (s *Server) komgaSSEEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE_UNSUPPORTED", "Streaming is not supported by this response writer.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	ch, unsubscribe := s.sse.subscribe()
	defer unsubscribe()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, open := <-ch:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Event, evt.Data); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
