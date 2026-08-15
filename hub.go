package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

const maxClients = 2

// A passcode is 64 bits of randomness (see store.go), which alone makes
// brute-forcing infeasible — this rate limit is a cheap second line of
// defense, capping how many wrong guesses this hub will even look at.
const (
	maxFailedAttempts    = 5
	failedAttemptsWindow = time.Minute
)

// registration carries a connecting Client, plus whatever token and/or
// passcode it presented, and a channel the Hub uses to reply with the
// outcome.
type registration struct {
	client   *Client
	token    string
	passcode string
	result   chan registrationResult
}

// registrationResult is the Hub's reply to a registration attempt. Reason
// is only meaningful when OK is false: "denied" (wrong or missing
// passcode) vs "full" (both seats are already taken by live connections).
type registrationResult struct {
	ok     bool
	reason string
}

// validPasscode compares in constant time so a failed attempt can't be used
// to guess the passcode one byte at a time via response-timing.
func validPasscode(p string) bool {
	return subtle.ConstantTimeCompare([]byte(p), []byte(passcode)) == 1
}

// Hub owns the set of connected clients. It is the only thing that ever
// touches the clients/seats/history state, so no mutex is needed —
// everything happens sequentially inside run()'s select loop.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan registration
	unregister chan *Client

	// done is closed exactly once, the instant this chat is permanently
	// closed and run() returns. Every goroutine that ever sends into one of
	// this Hub's channels (readPump's unregister/broadcast sends,
	// wsHandler's register send) also selects on done, so nobody blocks
	// forever trying to talk to a hub that's stopped listening.
	done chan struct{}

	// seats is the record of who's allowed in this chat — token -> assigned
	// user id. Entries are never removed just because a connection drops,
	// so a random visitor can never take a freed-up slot on disconnect
	// alone. The one exception: offlineSeat() lets a *passcode-holder* take
	// over a seat that currently has no live connection, so the real
	// second person can recover their spot from a new device. Persisted to
	// disk so it survives server restarts too.
	seats  map[string]string
	nextID int

	// history is every chat message ever sent, replayed to a client the
	// moment it (re)connects, so nobody misses messages sent while they
	// were away. Persisted to disk alongside seats.
	history []json.RawMessage

	// failedAt records recent failed-passcode timestamps, oldest first —
	// see rateLimited().
	failedAt []time.Time
}

func newHub() *Hub {
	seats := loadSeats()
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte),
		register:   make(chan registration),
		unregister: make(chan *Client),
		done:       make(chan struct{}),
		seats:      seats,
		nextID:     len(seats),
		history:    loadHistory(),
	}
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Printf("token generation error: %v", err)
	}
	return hex.EncodeToString(b)
}

// messageType extracts the "type" field from a raw broadcast message — the
// only thing the Hub ever inspects about message content, and only to
// decide what's worth persisting and whether it's a "close" command.
func messageType(raw []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	return envelope.Type
}

// rateLimited reports whether this hub has seen too many failed passcode
// attempts recently. It also prunes anything older than the window, so
// failedAt never grows without bound.
func (h *Hub) rateLimited() bool {
	cutoff := time.Now().Add(-failedAttemptsWindow)
	kept := h.failedAt[:0]
	for _, t := range h.failedAt {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	h.failedAt = kept
	return len(h.failedAt) >= maxFailedAttempts
}

func (h *Hub) recordFailure() {
	h.failedAt = append(h.failedAt, time.Now())
}

// offlineSeat returns a seat (token, id) that currently has no live
// connection attached to it, if one exists — used to let a passcode-holder
// take over a seat nobody is actively using right now.
func (h *Hub) offlineSeat() (token, id string, ok bool) {
	connected := make(map[string]bool, len(h.clients))
	for c := range h.clients {
		connected[c.id] = true
	}
	for t, id := range h.seats {
		if !connected[id] {
			return t, id, true
		}
	}
	return "", "", false
}

// sendWelcome sends this client its id/token and the full message history.
func (h *Hub) sendWelcome(c *Client) {
	welcome, err := json.Marshal(map[string]string{
		"type":  "welcome",
		"id":    c.id,
		"token": c.token,
	})
	if err != nil {
		log.Printf("welcome marshal error: %v", err)
	} else {
		c.send <- welcome
	}

	historyMsg, err := json.Marshal(map[string]any{
		"type":     "history",
		"messages": h.history,
	})
	if err != nil {
		log.Printf("history marshal error: %v", err)
		return
	}
	c.send <- historyMsg
}

func (h *Hub) run() {
	for {
		select {
		case req := <-h.register:
			if id, ok := h.seats[req.token]; ok && req.token != "" {
				// Returning seat-holder, proven by the token their browser
				// already holds — no passcode needed, regardless of the
				// current live-connection count.
				req.client.id = id
				req.client.token = req.token
				h.clients[req.client] = true
				log.Printf("client reconnected: %s", id)
				h.sendWelcome(req.client)
				req.result <- registrationResult{ok: true}
				continue
			}

			// No valid token — this browser has to prove it belongs here
			// with the shared passcode instead.
			if h.rateLimited() || !validPasscode(req.passcode) {
				h.recordFailure()
				req.result <- registrationResult{ok: false, reason: "denied"}
				continue
			}

			if len(h.seats) < maxClients {
				h.nextID++
				id := fmt.Sprintf("user-%d", h.nextID)
				token := newToken()
				h.seats[token] = id
				saveSeats(h.seats)

				req.client.id = id
				req.client.token = token
				h.clients[req.client] = true
				log.Printf("client registered: %s", id)
				h.sendWelcome(req.client)
				req.result <- registrationResult{ok: true}
				continue
			}

			// Both seats already exist. A correct passcode plus an
			// offline seat means this is the real second person coming
			// back from a new device after losing their saved token —
			// hand them that seat's identity. A correct passcode can
			// never bump someone who's actively connected right now.
			if oldToken, id, ok := h.offlineSeat(); ok {
				delete(h.seats, oldToken)
				newTok := newToken()
				h.seats[newTok] = id
				saveSeats(h.seats)

				req.client.id = id
				req.client.token = newTok
				h.clients[req.client] = true
				log.Printf("client reclaimed seat: %s", id)
				h.sendWelcome(req.client)
				req.result <- registrationResult{ok: true}
				continue
			}

			req.result <- registrationResult{ok: false, reason: "full"}

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				log.Println("client unregistered")
			}

		case message := <-h.broadcast:
			switch messageType(message) {
			case "close":
				closedMsg, _ := json.Marshal(map[string]string{"type": "closed"})
				for client := range h.clients {
					// Non-blocking, same as the normal broadcast below: a
					// stuck/full client must never be able to wedge this
					// goroutine, since nothing else can ever unwedge it.
					select {
					case client.send <- closedMsg:
					default:
					}
					close(client.send)
				}
				h.clients = nil

				wipeChat()
				log.Println("chat closed")

				close(h.done) // wake up anyone still trying to talk to us
				return        // stop selecting — this chat is done for good

			case "chat":
				h.history = append(h.history, json.RawMessage(message))
				appendMessage(message)
			}

			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					// client's buffer is full — it's stuck/dead, drop it
					// instead of blocking the whole hub.
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}
