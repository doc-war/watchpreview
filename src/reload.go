package main

import "sync"

type ReloadHub struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func NewReloadHub() *ReloadHub {
	return &ReloadHub{clients: make(map[chan struct{}]struct{})}
}

func (h *ReloadHub) Subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *ReloadHub) Unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *ReloadHub) Reload() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
