# CLAUDE.md

This file gives Claude Code context for working in this repository.

## Project Overview

A live chat server written in Go, using WebSockets for real-time bidirectional
communication, plus a minimal HTML/JS frontend to exercise it. No code has
been written yet — this file documents the intended architecture and the
reasoning behind it so implementation stays consistent as it's built out.

## Working with the user

The user is a **beginner-to-intermediate Go developer** building this project
partly to learn. When implementing features, fixing bugs, or making
architectural decisions in this repo:

- Explain the **reasoning** behind a choice (why this approach over the
  obvious alternative), not just the mechanics of the code.
- Call out Go-specific idioms as they come up (goroutines, channels, `select`,
  interfaces, `defer`) — assume familiarity with basic syntax but not with
  concurrency patterns or idiomatic Go project layout.
- Prefer showing the tradeoff (what breaks if you did it the "naive" way)
  over stating rules as given facts.

## Tech Stack

- **Language:** Go
- **WebSocket library:** `gorilla/websocket` — the de facto standard; the
  standard library has no WebSocket support, and `gorilla/websocket` is the
  most battle-tested option (also still maintained under `coder/websocket` as
  an alternative, but gorilla is what most Go chat tutorials and production
  code use).
- **Frontend:** plain HTML + vanilla JS (`WebSocket` browser API). No
  framework — the frontend exists to exercise the server, not as a product
  in its own right.

## Architecture

### The Hub-and-Client pattern

The server is built around three cooperating pieces:

1. **`Hub`** — a single goroutine that owns the set of connected clients and
   is the only thing that mutates it.
2. **`Client`** — one instance per connected browser, wrapping a
   `*websocket.Conn` with two goroutines (`readPump`, `writePump`).
3. **HTTP handler** — upgrades an incoming HTTP request to a WebSocket
   connection and hands it off to a new `Client`.

```
Browser <--WS--> HTTP handler --> Client{readPump, writePump} <--channels--> Hub
```

**Why a Hub goroutine instead of a `sync.Mutex` around a shared map?**
Both work, but the channel-based hub is the idiomatic Go answer to "share
state safely across goroutines": *share memory by communicating, don't
communicate by sharing memory.* With a mutex, every goroutine that touches
the client list needs to remember to lock/unlock correctly, and it's easy to
introduce a deadlock (e.g. locking inside a handler that also tries to send
on a channel another locked goroutine is waiting on). With the hub pattern,
the map is only ever touched by the hub's own goroutine — register,
unregister, and broadcast are just messages it processes one at a time via
`select`. No lock can ever be forgotten because there's nothing to lock.

**Why does each `Client` need two goroutines (read + write) instead of one?**
`gorilla/websocket`'s `*Conn` is **not safe for concurrent writes** — if two
goroutines call `WriteMessage` on the same connection at once, the frames can
interleave and corrupt the stream. So all writes to a given connection must
be serialized through a single goroutine (`writePump`), fed by a buffered
`chan []byte` (`client.send`). Reading, meanwhile, is a blocking call
(`ReadMessage`) that has to run continuously to detect disconnects and
incoming messages — it can't share a goroutine with the write loop without
one blocking the other. Hence: one goroutine per direction.

**Why is `client.send` a *buffered* channel?**
An unbuffered channel would make the hub's broadcast loop block until the
slow client's writePump was ready to receive — one slow or stalled browser
tab would stall the entire chat room. A buffered channel (e.g. capacity 256)
absorbs short bursts. If the buffer fills anyway (client is truly stuck/dead),
the hub detects that with a non-blocking `select`/`default` and drops the
client rather than blocking forever. This is a deliberate tradeoff: a client
that can't keep up gets disconnected instead of degrading the experience for
everyone else.

### Planned file layout

```
/cmd/server/main.go   - entrypoint: wires up hub + HTTP routes, starts server
/internal/chat/hub.go     - Hub struct + run loop
/internal/chat/client.go  - Client struct + readPump/writePump
/static/index.html    - minimal chat UI (plain JS WebSocket client)
go.mod
```

`internal/` is used so the chat package can't be imported by other modules —
appropriate here since this is a standalone server, not a library meant for
reuse.

### Not yet decided / open questions for future sessions

These are explicitly deferred, not forgotten:

- **Message format** — currently raw `[]byte` end-to-end. Will likely move to
  a small JSON envelope (`{type, room, sender, body, ts}`) once more than one
  message type exists (e.g. join/leave notifications vs chat text).
- **Rooms** — the current single-`Hub` design broadcasts to *all* clients.
  Multi-room support means either multiple hubs or a `room` field the hub
  filters on — not yet decided which.
- **Auth** — no authentication yet. Origin checking in the upgrader is
  currently permissive (`CheckOrigin` returns `true`), which is fine for
  local development only and must be tightened before any real deployment.
- **Heartbeats** — no ping/pong keepalive yet, so dead connections (e.g.
  laptop closed lid) aren't detected until the next failed write.

## Conventions

- Keep `Hub` and `Client` free of HTTP concerns — the HTTP handler is the
  only place that knows about `net/http`; this keeps the core chat logic
  testable without spinning up a real server.
- Prefer explaining *why* in comments only for non-obvious concurrency
  decisions (buffer sizes, channel direction, goroutine ownership) — not for
  restating what a line of code does.
