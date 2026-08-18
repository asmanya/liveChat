# Live Chat

A tiny two-person, link-based live chat server written in Go, using
WebSockets for real-time messaging. No accounts, no database — just a
shared link, a passcode, and a persistent conversation between exactly two
people.

## About

This is a private chat room for exactly two people. Host it once, and you
get a single link plus a passcode. Share both with the one other person you
want to talk to — the first two people to claim a seat with the correct
passcode own this chat permanently. Messages persist across restarts and
reconnects, so the conversation picks up right where it left off. Either
person can permanently end the chat, which wipes all stored messages and
seats for good — there's no reopening a closed chat.

## Features

- **Two-person rooms** — seats are claimed once and held permanently; a
  third visitor is turned away.
- **Passcode-gated access** — a shared secret is required to claim a seat,
  so having the link alone isn't enough to join.
- **Persistent history** — messages, seats, and the passcode survive server
  restarts, stored as plain files on disk.
- **Seat recovery** — if you lose your saved session (new device, cleared
  browser data), the passcode lets you reclaim your seat, but only if it's
  not currently in use by someone else.
- **Permanent close** — either person can end the chat for good; it wipes
  all stored data and the link stops working.
- **Read receipts** and a WhatsApp-style bubble UI.

## How it works

The server follows a hub-and-client pattern: a single `Hub` goroutine owns
the set of connected clients and all shared state (who's allowed in, the
message history), and every `Client` gets two goroutines — one pumping
messages off the WebSocket, one pumping messages onto it — so nothing ever
blocks the hub. See [CLAUDE.md](CLAUDE.md) for the full architecture
writeup and the reasoning behind it.

## Running locally

```
go run .
```

Starts on `:8080` by default. The passcode is generated on first run and
printed to the console (or set your own with `CHAT_PASSCODE`). Data is
stored under `./data` by default (override with `DATA_DIR`).

## Deploying

A `Dockerfile` is included for platforms like Fly.io or Railway that build
from a container image. Mount a persistent volume at `/data` so chat data
survives redeploys — see the Dockerfile for the exact path.

## Tech stack

- Go, [gorilla/websocket](https://github.com/gorilla/websocket)
- Plain HTML/CSS/JS frontend, no framework
- File-based storage — no database
