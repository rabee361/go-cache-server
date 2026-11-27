package main

import "sync"

var noReply = Reply{}

type PubSub struct {
	mu       sync.RWMutex
	channels map[string]map[*clientState]struct{}
}

func NewPubSub() *PubSub {
	return &PubSub{channels: make(map[string]map[*clientState]struct{})}
}

func (h *PubSub) remove(channel string, st *clientState) {
	if m := h.channels[channel]; m != nil {
		delete(m, st)

		if len(m) == 0 {
			delete(h.channels, channel)
		}
	}
}

func (h *PubSub) subscribe(channel string, st *clientState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.channels[channel] == nil {
		h.channels[channel] = make(map[*clientState]struct{})
	}
	h.channels[channel][st] = struct{}{}
}

func (h *PubSub) unsubscribe(channel string, st *clientState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remove(channel, st)
}

func (h *PubSub) removeClient(st *clientState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for channel := range st.subs {
		h.remove(channel, st)
	}
}

func (h *PubSub) publish(channel string, msg Reply) int {
	h.mu.RLock()
	subs := make([]*clientState, 0, len(h.channels[channel]))
	for st := range h.channels[channel] {
		subs = append(subs, st)
	}
	h.mu.RUnlock()
	for _, st := range subs {
		st.write(2, msg)
	}
	return len(subs)
}
