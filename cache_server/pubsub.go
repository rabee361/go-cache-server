package main

import "sync"

// noReply is returned by handlers that emit their own replies directly through
// state.write instead of a single return value. SUBSCRIBE/UNSUBSCRIBE send one
// confirmation per channel, so they can't be expressed as one Reply. The read
// loop recognises the zero Kind and writes nothing further.
var noReply = Reply{}

// PubSub is the server-wide registry of channel subscriptions. One hub is
// created per server (in serve) and shared by every connection, so a PUBLISH on
// one connection reaches subscribers sitting on other connections.
type PubSub struct {
	mu       sync.RWMutex
	channels map[string]map[*clientState]struct{}
}

func NewPubSub() *PubSub {
	return &PubSub{channels: make(map[string]map[*clientState]struct{})}
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

func (h *PubSub) remove(channel string, st *clientState) {
	if m := h.channels[channel]; m != nil {
		delete(m, st)
		
		if len(m) == 0 {
			delete(h.channels, channel)
		}
	}
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
		// Pushed messages always use the RESP2 array form; RESP3 push type (>)
		// is not implemented, matching the server's other RESP3 gaps.
		st.write(2, msg)
	}
	return len(subs)
}

func cmdSubscribe(store Store, state *clientState, args []string) Reply {
	for _, channel := range args[1:] {
		if _, ok := state.subs[channel]; !ok {
			state.subs[channel] = struct{}{}
			state.hub.subscribe(channel, state)
		}
		state.write(state.protocol_version, arrayReply([]Reply{
			bulkReply([]byte("subscribe")),
			bulkReply([]byte(channel)),
			intReply(int64(len(state.subs))),
		}))
	}
	return noReply
}

func cmdUnsubscribe(store Store, state *clientState, args []string) Reply {
	proto := state.protocol_version
	if len(args) == 1 {
		// No channels named: unsubscribe from all. Redis still emits exactly one
		// reply (nil channel, count 0) when the client wasn't subscribed to anything.
		if len(state.subs) == 0 {
			state.write(proto, arrayReply([]Reply{
				bulkReply([]byte("unsubscribe")), nullReply(), intReply(0),
			}))
			return noReply
		}
		for channel := range state.subs {
			state.hub.unsubscribe(channel, state)
			delete(state.subs, channel)
			state.write(proto, arrayReply([]Reply{
				bulkReply([]byte("unsubscribe")),
				bulkReply([]byte(channel)),
				intReply(int64(len(state.subs))),
			}))
		}
		return noReply
	}
	for _, channel := range args[1:] {
		if _, ok := state.subs[channel]; ok {
			state.hub.unsubscribe(channel, state)
			delete(state.subs, channel)
		}
		state.write(proto, arrayReply([]Reply{
			bulkReply([]byte("unsubscribe")),
			bulkReply([]byte(channel)),
			intReply(int64(len(state.subs))),
		}))
	}
	return noReply
}

func cmdPublish(store Store, state *clientState, args []string) Reply {
	channel, payload := args[1], args[2]
	receivers := state.hub.publish(channel, arrayReply([]Reply{
		bulkReply([]byte("message")),
		bulkReply([]byte(channel)),
		bulkReply([]byte(payload)),
	}))
	return intReply(int64(receivers))
}
