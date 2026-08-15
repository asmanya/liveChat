package main

import (
	"log"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait = 10 * time.Second

	// pongWait is how long we'll wait for a pong before deciding the
	// connection is dead. pingPeriod must stay well under pongWait so a
	// ping always has time to round-trip before the deadline fires.
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

// send is buffered so a slow reader on the other end doesn't block whoever
// is writing to this channel (the Hub, once it sends into it).
type Client struct {
	id    string
	token string
	hub   *Hub
	conn  *websocket.Conn
	send  chan []byte
}

func newClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
	}
}

// readPump pumps messages from the WebSocket connection to the Hub.
// It runs in its own goroutine and is the only reader of c.conn.
//
// It also owns dead-connection detection: without a read deadline, a
// connection that drops uncleanly (wifi cutting out, a laptop sleeping)
// never produces a read error, so ReadMessage blocks forever and the Hub
// keeps treating that seat as "connected" indefinitely — which would quietly
// break offlineSeat()'s ability to ever hand that seat back out. Resetting
// the deadline on every pong (browsers answer WebSocket pings automatically,
// no client-side JS needed) means a truly dead peer gets noticed within
// pongWait even if it never sends anything itself.
func (c *Client) readPump() {
	defer func() {
		select {
		case c.hub.unregister <- c:
		case <-c.hub.done:
			// room already closed — nothing left to unregister from
		}
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			log.Printf("read error: %v", err)
			break
		}
		log.Printf("received: %s", message)
		select {
		case c.hub.broadcast <- message:
		case <-c.hub.done:
			return
		}
	}
}

// writePump pumps messages from c.send to the WebSocket connection, and
// pings the peer every pingPeriod so readPump's deadline (on this end and,
// via the browser's automatic pong, implicitly on the other end) keeps
// getting renewed as long as the connection is actually alive.
// It runs in its own goroutine and is the only writer of c.conn, since
// *websocket.Conn is not safe for concurrent writes.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed our channel — hand off a clean close frame.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("write error: %v", err)
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
