package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"

	"github.com/gorilla/websocket"
)

const maxMessageBytes = 8192

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // no Origin header (e.g. non-browser client) — nothing to check
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
}

var hub = newHub()

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading to WebSocket: %v", err)
		return
	}
	conn.SetReadLimit(maxMessageBytes)

	token := r.URL.Query().Get("token")
	passcodeInput := r.URL.Query().Get("passcode")
	client := newClient(hub, conn)
	result := make(chan registrationResult, 1)

	select {
	case hub.register <- registration{client: client, token: token, passcode: passcodeInput, result: result}:
	case <-hub.done:
		// The chat was permanently closed — this link doesn't work anymore.
		closedMsg, _ := json.Marshal(map[string]string{"type": "closed"})
		conn.WriteMessage(websocket.TextMessage, closedMsg)
		conn.Close()
		return
	}

	if res := <-result; !res.ok {
		// res.reason is "full" (both seats live right now) or "denied"
		// (missing/wrong passcode) — the client shows a different message
		// for each.
		rejection, _ := json.Marshal(map[string]string{"type": res.reason})
		conn.WriteMessage(websocket.TextMessage, rejection)
		conn.Close()
		return
	}

	go client.writePump()
	client.readPump()
}

func main() {
	go hub.run()

	fs := http.FileServer(http.Dir("static"))
	http.Handle("/", fs)
	http.HandleFunc("/ws", wsHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("Server starting on %s", addr)
	log.Printf("Passcode: %s", passcode)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
