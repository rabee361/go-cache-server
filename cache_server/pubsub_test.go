package main

import (
	"reflect"
	"testing"
	"time"
)

func shareHub() (*clientState, *clientState, *PubSub) {
	hub := NewPubSub()
	a, b := newClientState(), newClientState()
	a.hub = hub
	b.hub = hub
	return a, b, hub
}

func subscribeAck(channel string, count int64) Reply {
	return arrayReply([]Reply{
		bulkReply([]byte("subscribe")),
		bulkReply([]byte(channel)),
		intReply(count),
	})
}

func unsubscribeAck(channel Reply, count int64) Reply {
	return arrayReply([]Reply{
		bulkReply([]byte("unsubscribe")),
		channel,
		intReply(count),
	})
}

func hubSubscribers(h *PubSub, channel string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.channels[channel])
}

func hubChannelCount(h *PubSub) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.channels)
}

func TestSubscribeAcks(t *testing.T) {
	a, _, hub := shareHub()
	store := newTestStore()

	if got := dispatch(store, a, []string{"SUBSCRIBE", "news"}); got.Kind != 0 {
		t.Fatalf("SUBSCRIBE reply = %+v, want no inline reply", got)
	}
	if len(a.pushed) != 1 || !reflect.DeepEqual(a.pushed[0], subscribeAck("news", 1)) {
		t.Fatalf("pushes = %+v, want a single [subscribe news 1] ack", a.pushed)
	}

	// Subscribing again re-acks with the unchanged count and does not duplicate
	// the registry entry.
	dispatch(store, a, []string{"SUBSCRIBE", "news"})
	if len(a.pushed) != 2 || !reflect.DeepEqual(a.pushed[1], subscribeAck("news", 1)) {
		t.Errorf("duplicate subscribe push = %+v, want [subscribe news 1]", a.pushed)
	}
	if n := hubSubscribers(hub, "news"); n != 1 {
		t.Errorf("hub subscribers for news = %d, want 1", n)
	}

	// One command can subscribe to several channels: counts grow per channel.
	dispatch(store, a, []string{"SUBSCRIBE", "weather", "sports"})
	if len(a.pushed) != 4 {
		t.Fatalf("pushes = %d, want 4", len(a.pushed))
	}
	if !reflect.DeepEqual(a.pushed[2], subscribeAck("weather", 2)) {
		t.Errorf("weather ack = %+v, want count 2", a.pushed[2])
	}
	if !reflect.DeepEqual(a.pushed[3], subscribeAck("sports", 3)) {
		t.Errorf("sports ack = %+v, want count 3", a.pushed[3])
	}
}

func TestUnsubscribeReplies(t *testing.T) {
	store := newTestStore()

	t.Run("no_subscriptions", func(t *testing.T) {
		a, _, _ := shareHub()
		if got := dispatch(store, a, []string{"UNSUBSCRIBE"}); got.Kind != 0 {
			t.Fatalf("UNSUBSCRIBE reply = %+v, want no inline reply", got)
		}
		want := unsubscribeAck(nullReply(), 0)
		if len(a.pushed) != 1 || !reflect.DeepEqual(a.pushed[0], want) {
			t.Errorf("pushes = %+v, want [unsubscribe nil 0]", a.pushed)
		}
	})

	t.Run("unknown_channel_while_subscribed", func(t *testing.T) {
		a, _, _ := shareHub()
		dispatch(store, a, []string{"SUBSCRIBE", "news"})
		dispatch(store, a, []string{"UNSUBSCRIBE", "other"})
		want := unsubscribeAck(bulkReply([]byte("other")), 1)
		if len(a.pushed) != 2 || !reflect.DeepEqual(a.pushed[1], want) {
			t.Errorf("pushes = %+v, want [unsubscribe other 1]", a.pushed)
		}
	})

	t.Run("named_channel_removes_subscription", func(t *testing.T) {
		a, _, _ := shareHub()
		dispatch(store, a, []string{"SUBSCRIBE", "news"})
		dispatch(store, a, []string{"UNSUBSCRIBE", "news"})
		want := unsubscribeAck(bulkReply([]byte("news")), 0)
		if len(a.pushed) != 2 || !reflect.DeepEqual(a.pushed[1], want) {
			t.Fatalf("pushes = %+v, want [unsubscribe news 0]", a.pushed)
		}
		if len(a.subs) != 0 {
			t.Errorf("subs = %v, want empty", a.subs)
		}
		if got := dispatch(store, a, []string{"PUBLISH", "news", "x"}); !reflect.DeepEqual(got, intReply(0)) {
			t.Errorf("PUBLISH after unsubscribe = %+v, want :0", got)
		}
	})

	t.Run("bare_unsubscribes_all", func(t *testing.T) {
		a, _, _ := shareHub()
		dispatch(store, a, []string{"SUBSCRIBE", "one", "two"})
		dispatch(store, a, []string{"UNSUBSCRIBE"})

		if len(a.pushed) != 4 {
			t.Fatalf("pushes = %d, want 4 (two acks, two unsubscribe acks)", len(a.pushed))
		}
		// Map iteration order is unspecified, so compare the pair as a set.
		gotA, gotB := a.pushed[2], a.pushed[3]
		if !reflect.DeepEqual(gotA.Arr[2], intReply(1)) || !reflect.DeepEqual(gotB.Arr[2], intReply(0)) {
			t.Errorf("unsubscribe counts = %v, %v; want 1 then 0", gotA.Arr[2], gotB.Arr[2])
		}
		nameA, nameB := string(gotA.Arr[1].Str), string(gotB.Arr[1].Str)
		if nameA == nameB || (nameA != "one" && nameA != "two") || (nameB != "one" && nameB != "two") {
			t.Errorf("unsubscribe channel names = %q, %q; want one and two in either order", nameA, nameB)
		}
		if got := dispatch(store, a, []string{"PUBLISH", "one", "x"}); !reflect.DeepEqual(got, intReply(0)) {
			t.Errorf("PUBLISH after bare UNSUBSCRIBE = %+v, want :0", got)
		}
	})
}

func TestPublishDelivery(t *testing.T) {
	hub := NewPubSub()
	a, b, c := newClientState(), newClientState(), newClientState()
	for _, st := range []*clientState{a, b, c} {
		st.hub = hub
	}
	store := newTestStore()

	dispatch(store, a, []string{"SUBSCRIBE", "foo"})
	dispatch(store, c, []string{"SUBSCRIBE", "foo"})
	dispatch(store, b, []string{"SUBSCRIBE", "bar"})

	// b publishes to foo; only a and c receive the message.
	if got := dispatch(store, b, []string{"PUBLISH", "foo", "hi"}); !reflect.DeepEqual(got, intReply(2)) {
		t.Fatalf("PUBLISH = %+v, want :2", got)
	}
	msg := arrayReply([]Reply{
		bulkReply([]byte("message")),
		bulkReply([]byte("foo")),
		bulkReply([]byte("hi")),
	})
	for name, st := range map[string]*clientState{"a": a, "c": c} {
		if len(st.pushed) != 2 {
			t.Fatalf("%s pushes = %d, want ack + message", name, len(st.pushed))
		}
		if !reflect.DeepEqual(st.pushed[1], msg) {
			t.Errorf("%s message = %+v, want %+v", name, st.pushed[1], msg)
		}
	}
	if len(b.pushed) != 1 {
		t.Errorf("publisher b pushes = %d, want only its own subscribe ack", len(b.pushed))
	}
}

func TestPublishZeroSubscribers(t *testing.T) {
	a, _, _ := shareHub()
	if got := dispatch(newTestStore(), a, []string{"PUBLISH", "ghosts", "boo"}); !reflect.DeepEqual(got, intReply(0)) {
		t.Errorf("PUBLISH = %+v, want :0", got)
	}
	if len(a.pushed) != 0 {
		t.Errorf("pushes = %+v, want none", a.pushed)
	}
}

func TestPubSubRemoveClient(t *testing.T) {
	a, b, hub := shareHub()
	store := newTestStore()
	dispatch(store, a, []string{"SUBSCRIBE", "ch"})
	dispatch(store, b, []string{"SUBSCRIBE", "ch"})

	hub.removeClient(a)
	if got := dispatch(store, b, []string{"PUBLISH", "ch", "x"}); !reflect.DeepEqual(got, intReply(1)) {
		t.Errorf("PUBLISH after removing a = %+v, want :1", got)
	}

	hub.removeClient(b)
	if got := dispatch(store, b, []string{"PUBLISH", "ch", "x"}); !reflect.DeepEqual(got, intReply(0)) {
		t.Errorf("PUBLISH after removing b = %+v, want :0", got)
	}

	// Removing a client that never subscribed is a no-op.
	hub.removeClient(newClientState())
	if n := hubChannelCount(hub); n != 0 {
		t.Errorf("hub channels = %d, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// Pub/sub over the wire
// ---------------------------------------------------------------------------

// Socket writes are serialized per connection by handleConn's wmu, and
// hub.publish writes the message to every subscriber before the publisher's
// count reply is written. Normal delivery therefore needs no sleeps here.
// Only disconnect cleanup is asynchronous (it runs in the defer of a dead
// handler goroutine), which is why that one test polls.

func TestE2EPubSubMessage(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	sub, subR := dialServer(t, addr)
	pub, pubR := dialServer(t, addr)

	sendBytes(t, sub, []byte("*2\r\n$9\r\nSUBSCRIBE\r\n$2\r\nch\r\n"))
	wantAck := "*3\r\n$9\r\nsubscribe\r\n$2\r\nch\r\n:1\r\n"
	if got := readResp(t, subR); got != wantAck {
		t.Fatalf("subscribe ack = %q, want %q", got, wantAck)
	}

	// The ack above proves the subscriber is registered; publish now delivers.
	sendBytes(t, pub, []byte("*3\r\n$7\r\nPUBLISH\r\n$2\r\nch\r\n$5\r\nhello\r\n"))
	if got := readResp(t, pubR); got != ":1\r\n" {
		t.Errorf("PUBLISH reply = %q, want \":1\\r\\n\"", got)
	}
	wantMsg := "*3\r\n$7\r\nmessage\r\n$2\r\nch\r\n$5\r\nhello\r\n"
	if got := readResp(t, subR); got != wantMsg {
		t.Errorf("delivered message = %q, want %q", got, wantMsg)
	}
}

func TestE2EPublishNoSubscribers(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	conn, r := dialServer(t, addr)
	sendBytes(t, conn, []byte("*3\r\n$7\r\nPUBLISH\r\n$6\r\nghosts\r\n$3\r\nboo\r\n"))
	if got := readResp(t, r); got != ":0\r\n" {
		t.Errorf("PUBLISH reply = %q, want \":0\\r\\n\"", got)
	}
}

func TestE2EPubSubDisconnectCleanup(t *testing.T) {
	addr := startTestServer(t, newTestStore())
	sub, subR := dialServer(t, addr)
	sendBytes(t, sub, []byte("*2\r\n$9\r\nSUBSCRIBE\r\n$2\r\nch\r\n"))
	if got := readResp(t, subR); got != "*3\r\n$9\r\nsubscribe\r\n$2\r\nch\r\n:1\r\n" {
		t.Fatalf("subscribe ack = %q", got)
	}
	sub.Close()

	pub, pubR := dialServer(t, addr)
	deadline := time.Now().Add(2 * time.Second)
	for {
		sendBytes(t, pub, []byte("*3\r\n$7\r\nPUBLISH\r\n$2\r\nch\r\n$1\r\nx\r\n"))
		switch got := readResp(t, pubR); got {
		case ":0\r\n":
			return // cleanup has run and the hub entry is gone
		case ":1\r\n":
			// The handler has not noticed the close yet; retry briefly.
		default:
			t.Fatalf("PUBLISH reply = %q, want \":0\\r\\n\" or \":1\\r\\n\"", got)
		}
		if time.Now().After(deadline) {
			t.Fatal("subscriber was not removed from the hub within 2s of disconnecting")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
