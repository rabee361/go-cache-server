package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
)

type commandHandler func(store Store, state *clientState, args []string) Reply

type clientState struct {
	protocol_version int
	id               int64
}

var connIDCounter atomic.Int64

func newClientState() *clientState {
	return &clientState{protocol_version: 2, id: connIDCounter.Add(1)}
}

type commandSpec struct {
	name    string
	minArgs int
	maxArgs int
	handler commandHandler
}

var commandTable = map[string]commandSpec{
	"SET":    {"SET", 3, -1, cmdSet},
	"GET":    {"GET", 2, 2, cmdGet},
	"DEL":    {"DEL", 2, -1, cmdDel},
	"HELLO":  {"HELLO", 1, -1, cmdHello},
	"CLIENT": {"CLIENT", 2, -1, cmdClient},
}

func dispatch(store Store, state *clientState, args []string) Reply {
	if len(args) == 0 {
		return errReply("ERR empty command")
	}
	name := strings.ToUpper(args[0])
	spec, ok := commandTable[name]
	if !ok {
		return errReply(fmt.Sprintf("ERR unknown command '%s'", args[0]))
	}
	if len(args) < spec.minArgs || (spec.maxArgs >= 0 && len(args) > spec.maxArgs) {
		return errReply(fmt.Sprintf("ERR wrong number of arguments for '%s' command", name))
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

func cmdHello(store Store, state *clientState, args []string) Reply {
	if len(args) == 1 {
		return helloReply(state)
	}
	protocol_version, err := strconv.Atoi(args[1])
	if err != nil || (protocol_version != 2 && protocol_version != 3) {
		return errReply("NOPROTO unsupported protocol version")
	}
	if len(args) > 2 {
		opt := strings.ToUpper(args[2])
		if opt == "AUTH" {
			return errReply("ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?")
		}
		return errReply("ERR unsupported HELLO option '" + opt + "'")
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

func cmdClient(store Store, state *clientState, args []string) Reply {
	switch strings.ToUpper(args[1]) {
	case "SETINFO":
		if len(args) != 4 {
			return errReply("ERR wrong number of arguments for 'client|setinfo' command")
		}
		return simpleString("OK")
	default:
		return errReply(fmt.Sprintf("ERR Unknown subcommand or wrong number of arguments for '%s'. Try CLIENT HELP.", args[1]))
	}
}
