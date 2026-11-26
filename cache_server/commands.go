package main

import (
	"errors"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type commandHandler func(store Store, state *clientState, args []string) Reply

type clientState struct {
	protocol_version int
	id               int64
	inMulti          bool // a MULTI is open on this connection
	dirty            bool // a queue-time error (unknown cmd / bad arity) marks the transaction unexecutable
	queue            []queuedCommand

	// pub/sub
	hub    *PubSub                  // shared server registry; newClientState gives tests a private one
	subs   map[string]struct{}      // channels this client is currently subscribed to
	write  func(proto int, r Reply) // emits one reply to this client; handleConn replaces it with the real, mutex-guarded socket writer
	pushed []Reply                  // default sink for write, so unit tests can inspect out-of-band replies
}

// queuedCommand captures the resolved handler at MULTI-queue time, so EXEC can
// run it without reading commandTable (which would be an init cycle, since
// commandTable references cmdExec).
type queuedCommand struct {
	handler commandHandler
	args    []string
}

var connIDCounter atomic.Int64

func newClientState() *clientState {
	st := &clientState{
		protocol_version: 2,
		id:               connIDCounter.Add(1),
		hub:              NewPubSub(),
		subs:             make(map[string]struct{}),
	}
	st.write = func(proto int, r Reply) { st.pushed = append(st.pushed, r) }
	return st
}

type commandSpec struct {
	name    string
	minArgs int
	maxArgs int
	handler commandHandler
}

var commandTable = map[string]commandSpec{
	"SET":           {"SET", 3, -1, cmdSet}, // EX, PX (NX, XX, KEEPTTL, GET planned)
	"GET":           {"GET", 2, 2, cmdGet},
	"DEL":           {"DEL", 2, -1, cmdDel},
	"EXISTS":        {"EXISTS", 2, -1, cmdExists},
	"FLUSHDB":       {"FLUSHDB", 1, 1, cmdFlushDB},
	"HELLO":         {"HELLO", 1, -1, cmdHello},
	"CLIENT":        {"CLIENT", 2, -1, cmdClient},
	"PING":          {"PING", 1, 2, cmdPing},
	"CONFIG":        {"CONFIG", 2, -1, cmdConfig},
	"LPUSH":         {"LPUSH", 3, -1, cmdLpush},
	"LLEN":          {"LLEN", 2, 2, cmdLlen},
	"BRPOP":         {"BRPOP", 3, -1, cmdBrpop},
	"LREM":          {"LREM", 4, 4, cmdLrem},
	"MULTI":         {"MULTI", 1, 1, cmdMulti},
	"EXEC":          {"EXEC", 1, 1, cmdExec},
	"DISCARD":       {"DISCARD", 1, 1, cmdDiscard},
	"SADD":          {"SADD", 3, -1, cmdSadd},
	"ZADD":          {"ZADD", 4, -1, cmdZadd},
	"ZRANGEBYSCORE": {"ZRANGEBYSCORE", 4, 4, cmdZRangeByScore},
	"SUBSCRIBE":     {"SUBSCRIBE", 2, -1, cmdSubscribe},
	"UNSUBSCRIBE":   {"UNSUBSCRIBE", 1, -1, cmdUnsubscribe},
	"PUBLISH":       {"PUBLISH", 3, 3, cmdPublish},
}

func dispatch(store Store, state *clientState, args []string) Reply {
	if len(args) == 0 {
		return errReply("ERR empty command")
	}
	name := strings.ToUpper(args[0])
	spec, ok := commandTable[name]
	if !ok {
		if state.inMulti {
			state.dirty = true // EXEC will abort the whole transaction
		}
		return errReply(fmt.Sprintf("ERR unknown command '%s'", args[0]))
	}
	if len(args) < spec.minArgs || (spec.maxArgs >= 0 && len(args) > spec.maxArgs) {
		if state.inMulti {
			state.dirty = true
		}
		return errReply(fmt.Sprintf("ERR wrong number of arguments for '%s' command", name))
	}
	// Inside a transaction, buffer everything except the control commands themselves.
	if state.inMulti && name != "MULTI" && name != "EXEC" && name != "DISCARD" {
		state.queue = append(state.queue, queuedCommand{handler: spec.handler, args: args})
		return simpleString("QUEUED")
	}
	return spec.handler(store, state, args)
}

func cmdSet(store Store, state *clientState, args []string) Reply {
	key, value := args[1], []byte(args[2])
	var ttlMs int64
	ttlSet := false
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "EX", "PX":
			if ttlSet || i+1 >= len(args) {
				return errReply("ERR syntax error")
			}
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || n <= 0 {
				return errReply("ERR invalid expire time in 'set' command")
			}
			if strings.EqualFold(args[i], "EX") {
				if n > math.MaxInt64/1000 {
					return errReply("ERR invalid expire time in 'set' command")
				}
				ttlMs = n * 1000
			} else {
				ttlMs = n
			}
			ttlSet = true
			i++
		default:
			return errReply("ERR syntax error")
		}
	}
	if err := store.Set(key, value, ttlMs); err != nil {
		if errors.Is(err, ErrOOM) {
			return errReply(err.Error())
		}
		return errReply("ERR set failed: " + err.Error())
	}
	return simpleString("OK")
}

func cmdGet(store Store, state *clientState, args []string) Reply {
	v, ok := store.Get(args[1])
	if !ok {
		return nullReply()
	}
	return bulkReply(v)
}

func cmdDel(store Store, state *clientState, args []string) Reply {
	var n int64
	for _, k := range args[1:] {
		c, err := store.Delete(k)
		if err != nil {
			return errReply("ERR delete failed: " + err.Error())
		}
		n += int64(c)
	}
	return intReply(n)
}

func cmdExists(store Store, state *clientState, args []string) Reply {
	var n int64
	for _, k := range args[1:] {
		c, err := store.Exists(k)
		if err != nil {
			return errReply("ERR exists failed: " + err.Error())
		}
		n += int64(c)
	}
	return intReply(n)
}

func cmdFlushDB(store Store, state *clientState, args []string) Reply {
	if err := store.FlushDB(); err != nil {
		return errReply("ERR flushdb failed: " + err.Error())
	}
	return simpleString("OK")
}

func cmdHello(store Store, state *clientState, args []string) Reply {
	if len(args) == 1 {
		return helloReply(state)
	}
	protocol_version, err := strconv.Atoi(args[1])
	if err != nil || (protocol_version != 2 && protocol_version != 3) {
		return errReply("NOPROTO unsupported protocol version")
	}
	for i := 2; i < len(args); i++ {
		opt := strings.ToUpper(args[i])
		if opt != "AUTH" {
			return errReply(fmt.Sprintf("Syntax error in HELLO option '%s'", args[i]))
		}
		if i+2 >= len(args) {
			return errReply(fmt.Sprintf("Syntax error in HELLO option '%s'", args[i]))
		}
		if args[i+1] != "default" {
			return errReply("WRONGPASS invalid username-password pair or user is disabled.")
		}
		i += 2
	}
	state.protocol_version = protocol_version
	return helloReply(state)
}

func helloReply(state *clientState) Reply {
	return mapReply([]Reply{
		bulkReply([]byte("server")), bulkReply([]byte("cache-server")),
		bulkReply([]byte("version")), bulkReply([]byte("0.1.0")),
		bulkReply([]byte("proto")), intReply(int64(state.protocol_version)),
		bulkReply([]byte("id")), intReply(state.id),
		bulkReply([]byte("mode")), bulkReply([]byte("standalone")),
		bulkReply([]byte("role")), bulkReply([]byte("master")),
		bulkReply([]byte("modules")), arrayReply(nil),
	})
}

func cmdPing(store Store, state *clientState, args []string) Reply {
	if len(args) == 2 {
		return bulkReply([]byte(args[1]))
	}
	return simpleString("PONG")
}

func parseMemSize(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "kb"):
		s, mult = s[:len(s)-2], 1024
	case strings.HasSuffix(s, "mb"):
		s, mult = s[:len(s)-2], 1024*1024
	case strings.HasSuffix(s, "gb"):
		s, mult = s[:len(s)-2], 1024*1024*1024
	case strings.HasSuffix(s, "b"):
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "k"):
		s, mult = s[:len(s)-1], 1024
	case strings.HasSuffix(s, "m"):
		s, mult = s[:len(s)-1], 1024*1024
	case strings.HasSuffix(s, "g"):
		s, mult = s[:len(s)-1], 1024*1024*1024
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("cannot parse %q as a memory size", s)
	}
	if mult > 1 && n > math.MaxInt64/mult {
		return 0, fmt.Errorf("memory size %q overflows int64", s)
	}
	return n * mult, nil
}

func cmdConfig(store Store, state *clientState, args []string) Reply {
	switch strings.ToUpper(args[1]) {
	case "GET":
		if len(args) != 3 {
			return errReply("ERR wrong number of arguments for 'config|get' command")
		}
		pattern := args[2]
		params := []struct{ name, value string }{
			{"maxmemory", strconv.FormatInt(serverConfig.MaxMemory(), 10)},
			{"maxclients", strconv.Itoa(serverConfig.MaxClients())},
		}
		var out []Reply
		for _, p := range params {
			matched, err := path.Match(pattern, p.name)
			if err != nil {
				return errReply("ERR Invalid glob pattern: " + err.Error())
			}
			if matched {
				out = append(out, bulkReply([]byte(p.name)), bulkReply([]byte(p.value)))
			}
		}
		if state.protocol_version >= 3 {
			return mapReply(out)
		}
		return arrayReply(out)
	case "SET":
		if len(args) < 4 || len(args)%2 != 0 {
			return errReply("ERR wrong number of arguments for 'config|set' command")
		}
		type change struct {
			name string
			n    int64
		}
		var changes []change
		for i := 2; i < len(args); i += 2 {
			name := strings.ToLower(args[i])
			switch name {
			case "maxmemory":
				n, err := parseMemSize(args[i+1])
				if err != nil {
					return errReply("ERR argument couldn't be parsed into an integer")
				}
				changes = append(changes, change{name, n})
			case "maxclients":
				n, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || n <= 0 {
					return errReply("ERR argument couldn't be parsed into an integer")
				}
				changes = append(changes, change{name, n})
			default:
				return errReply(fmt.Sprintf("ERR Unsupported CONFIG parameter: %s", name))
			}
		}
		for _, c := range changes {
			switch c.name {
			case "maxmemory":
				serverConfig.SetMaxMemory(c.n)
			case "maxclients":
				serverConfig.SetMaxClients(int(c.n))
			}
		}
		return simpleString("OK")
	default:
		return errReply(fmt.Sprintf("ERR Unknown subcommand or wrong number of arguments for '%s'. Try CONFIG HELP.", args[1]))
	}
}

func cmdLpush(store Store, state *clientState, args []string) Reply {
	n, err := store.LPush(args[1], args[2:]...)
	if err != nil {
		return errReply(err.Error())
	}
	return intReply(n)
}

func cmdLlen(store Store, state *clientState, args []string) Reply {
	n, err := store.LLEN(args[1])
	if err != nil {
		return errReply(err.Error())
	}
	return intReply(n)
}

const brpopPollInterval = 50 * time.Millisecond

func cmdBrpop(store Store, state *clientState, args []string) Reply {
	timeoutSec, err := strconv.ParseFloat(args[len(args)-1], 64)
	if err != nil || timeoutSec < 0 {
		return errReply("ERR timeout is not a float or out of range")
	}
	keys := args[1 : len(args)-1]
	deadline := time.Now().Add(time.Duration(timeoutSec * float64(time.Second)))
	for {
		for _, k := range keys {
			if v, ok := store.RPop(k); ok {
				return arrayReply([]Reply{bulkReply([]byte(k)), bulkReply([]byte(v))})
			}
		}
		if timeoutSec == 0 {
			time.Sleep(brpopPollInterval)
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nullArrayReply()
		}
		if remaining > brpopPollInterval {
			remaining = brpopPollInterval
		}
		time.Sleep(remaining)
	}
}

func cmdLrem(store Store, state *clientState, args []string) Reply {
	count, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return errReply("ERR value is not an integer or out of range")
	}
	n, err := store.LRem(args[1], count, args[3])
	if err != nil {
		return errReply(err.Error())
	}
	return intReply(n)
}

func cmdMulti(store Store, state *clientState, args []string) Reply {
	if state.inMulti {
		return errReply("ERR MULTI calls can not be nested")
	}
	state.inMulti = true
	state.dirty = false
	state.queue = nil
	return simpleString("OK")
}

func cmdExec(store Store, state *clientState, args []string) Reply {
	if !state.inMulti {
		return errReply("ERR EXEC without MULTI")
	}
	queue, dirty := state.queue, state.dirty
	state.inMulti, state.dirty, state.queue = false, false, nil
	if dirty {
		return errReply("EXECABORT Transaction discarded because of previous errors.")
	}
	results := make([]Reply, 0, len(queue))
	for _, qc := range queue {
		r := qc.handler(store, state, qc.args)
		if r.Kind == 0 {
			// Handler (pub/sub) already emitted its own replies out of band.
			continue
		}
		results = append(results, r)
	}
	return arrayReply(results)
}

func cmdDiscard(store Store, state *clientState, args []string) Reply {
	if !state.inMulti {
		return errReply("ERR DISCARD without MULTI")
	}
	state.inMulti, state.dirty, state.queue = false, false, nil
	return simpleString("OK")
}

func cmdSadd(store Store, state *clientState, args []string) Reply {
	n, err := store.SAdd(args[1], args[2:]...)
	if err != nil {
		return errReply(err.Error())
	}
	return intReply(n)
}

func cmdZadd(store Store, state *clientState, args []string) Reply {
	if (len(args)-2)%2 != 0 {
		return errReply("ERR syntax error")
	}
	type pair struct {
		member string
		score  float64
	}
	pairs := make([]pair, 0, (len(args)-2)/2)
	for i := 2; i < len(args); i += 2 {
		score, err := strconv.ParseFloat(args[i], 64)
		if err != nil {
			return errReply("ERR value is not a valid float")
		}
		pairs = append(pairs, pair{args[i+1], score})
	}
	var added int64
	for _, p := range pairs {
		n, err := store.ZAdd(args[1], p.member, p.score)
		if err != nil {
			return errReply(err.Error())
		}
		added += n
	}
	return intReply(added)
}

func parseScoreBound(s string) (val float64, excl bool, err error) {
	if strings.HasPrefix(s, "(") {
		excl = true
		s = s[1:]
	}
	switch strings.ToLower(s) {
	case "-inf":
		return math.Inf(-1), excl, nil
	case "+inf", "inf":
		return math.Inf(1), excl, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false, fmt.Errorf("not a float: %q", s)
	}
	return v, excl, nil
}

func cmdZRangeByScore(store Store, state *clientState, args []string) Reply {
	minScore, minExcl, err := parseScoreBound(args[2])
	if err != nil {
		return errReply("ERR min or max is not a float")
	}
	maxScore, maxExcl, err := parseScoreBound(args[3])
	if err != nil {
		return errReply("ERR min or max is not a float")
	}
	members, err := store.ZRangeByScore(args[1], minScore, maxScore, minExcl, maxExcl)
	if err != nil {
		return errReply(err.Error())
	}
	out := make([]Reply, len(members))
	for i, m := range members {
		out[i] = bulkReply([]byte(m))
	}
	return arrayReply(out)
}

func cmdClient(store Store, state *clientState, args []string) Reply {
	switch strings.ToUpper(args[1]) {
	case "SETINFO":
		if len(args) != 4 {
			return errReply("ERR wrong number of arguments for 'client|setinfo' command")
		}
		return simpleString("OK")
	case "MAINT_NOTIFICATIONS":
		// redis-py 8.x sends `CLIENT MAINT_NOTIFICATIONS ON moving-endpoint-type <type>`
		// during connection setup. We never emit maintenance notifications, but must
		// acknowledge with +OK or the client's handshake fails.
		return simpleString("OK")
	default:
		return errReply(fmt.Sprintf("ERR Unknown subcommand or wrong number of arguments for '%s'. Try CLIENT HELP.", args[1]))
	}
}
