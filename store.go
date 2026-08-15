package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"strings"
)

// dataDir defaults to "data" locally; set DATA_DIR when deploying to point
// this at a mounted persistent volume (e.g. "/data" on Fly.io).
var (
	dataDir      = getDataDir()
	messagesFile = dataDir + "/messages.jsonl"
	seatsFile    = dataDir + "/seats.json"
	passcodeFile = dataDir + "/passcode.txt"
)

func getDataDir() string {
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		return dir
	}
	return "data"
}

// passcode gates who's allowed to claim a seat in the first place — a link
// alone isn't proof you're the intended person, so anyone without a saved
// seat token has to also know this. Set CHAT_PASSCODE explicitly, or one is
// generated once and persisted so it survives restarts; either way it's
// printed at startup so the host can read it off the console.
var passcode = loadOrCreatePasscode()

func loadOrCreatePasscode() string {
	if p := os.Getenv("CHAT_PASSCODE"); p != "" {
		return p
	}

	if data, err := os.ReadFile(passcodeFile); err == nil {
		return strings.TrimSpace(string(data))
	} else if !os.IsNotExist(err) {
		log.Printf("loadOrCreatePasscode read error: %v", err)
	}

	// 8 bytes (64 bits) — long enough that guessing it is infeasible even
	// without the rate limit in Hub.rateLimited(), which exists as a second
	// line of defense, not the only one.
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		log.Printf("passcode generation error: %v", err)
	}
	p := hex.EncodeToString(b)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Printf("loadOrCreatePasscode mkdir error: %v", err)
	} else if err := os.WriteFile(passcodeFile, []byte(p+"\n"), 0o644); err != nil {
		log.Printf("loadOrCreatePasscode write error: %v", err)
	}
	return p
}

type seatRecord struct {
	Token string `json:"token"`
	ID    string `json:"id"`
}

// loadHistory reads previously stored chat messages, in the order they were
// sent, from disk. Each line in messagesFile is one raw chat-message JSON
// object, so it's returned as json.RawMessage — the Hub never needs to know
// the shape of a chat message, only that it's valid JSON to re-embed later.
func loadHistory() []json.RawMessage {
	f, err := os.Open(messagesFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("loadHistory open error: %v", err)
		}
		return nil
	}
	defer f.Close()

	var history []json.RawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		history = append(history, json.RawMessage(line))
	}
	if err := scanner.Err(); err != nil {
		log.Printf("loadHistory scan error: %v", err)
	}
	return history
}

// appendMessage appends one chat message (raw JSON bytes) as a new line.
func appendMessage(message []byte) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Printf("appendMessage mkdir error: %v", err)
		return
	}
	f, err := os.OpenFile(messagesFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("appendMessage open error: %v", err)
		return
	}
	defer f.Close()

	if _, err := f.Write(message); err != nil {
		log.Printf("appendMessage write error: %v", err)
		return
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		log.Printf("appendMessage write error: %v", err)
	}
}

// wipeChat permanently deletes the saved messages and seat assignments —
// used when "Close chat" ends the chat for good.
func wipeChat() {
	if err := os.Remove(messagesFile); err != nil && !os.IsNotExist(err) {
		log.Printf("wipeChat remove messages error: %v", err)
	}
	if err := os.Remove(seatsFile); err != nil && !os.IsNotExist(err) {
		log.Printf("wipeChat remove seats error: %v", err)
	}
}

// loadSeats reads the persisted token -> user-id assignments.
func loadSeats() map[string]string {
	data, err := os.ReadFile(seatsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("loadSeats read error: %v", err)
		}
		return map[string]string{}
	}

	var records []seatRecord
	if err := json.Unmarshal(data, &records); err != nil {
		log.Printf("loadSeats unmarshal error: %v", err)
		return map[string]string{}
	}

	seats := make(map[string]string, len(records))
	for _, r := range records {
		seats[r.Token] = r.ID
	}
	return seats
}

// saveSeats overwrites the seats file with the full current token -> id map.
func saveSeats(seats map[string]string) {
	records := make([]seatRecord, 0, len(seats))
	for token, id := range seats {
		records = append(records, seatRecord{Token: token, ID: id})
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Printf("saveSeats marshal error: %v", err)
		return
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Printf("saveSeats mkdir error: %v", err)
		return
	}
	if err := os.WriteFile(seatsFile, data, 0o644); err != nil {
		log.Printf("saveSeats write error: %v", err)
	}
}
