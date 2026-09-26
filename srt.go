package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	srt "github.com/datarhei/gosrt"
	"github.com/gorilla/websocket"
)

type listenerConn struct {
	srt.Conn
	listener srt.Listener
}

func (lc listenerConn) Close() error {
	lc.listener.Close()
	return lc.Conn.Close()
}

type writer interface {
	io.WriteCloser
}

type nonblockingWriter struct {
	dst  io.WriteCloser
	buf  *bytes.Buffer
	lock sync.RWMutex
	size int
	done bool
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type hub struct {
	clients    map[*websocket.Conn]bool
	broadcast  chan []byte
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	mutex      sync.RWMutex

	// onMessage, if set, observes every broadcast message.
	onMessage func([]byte)
}

func newHub(onMessage func([]byte)) *hub {
	return &hub{
		clients:    make(map[*websocket.Conn]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
		onMessage:  onMessage,
	}
}

func (h *hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			h.clients[client] = true
			h.mutex.Unlock()
			log.Printf("WebSocket client connected. Total clients: %d", len(h.clients))

		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.Close()
			}
			h.mutex.Unlock()
			log.Printf("WebSocket client disconnected. Total clients: %d", len(h.clients))

		case message := <-h.broadcast:
			if h.onMessage != nil {
				h.onMessage(message)
			}
			h.mutex.RLock()
			for client := range h.clients {
				err := client.WriteMessage(websocket.TextMessage, message)
				if err != nil {
					delete(h.clients, client)
					client.Close()
				}
			}
			h.mutex.RUnlock()
		}
	}
}

type statsMessage struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"` // "writer" or "reader"
	Stats     *srt.Statistics `json:"stats"`
	Links     []linkStats     `json:"links,omitempty"` // SRTLA links feeding the reader
}

type stats struct {
	interval   time.Duration // reporting interval
	lastReport time.Time     // last time a report was sent

	reader io.ReadCloser
	writer io.WriteCloser
	hub    *hub
}

func (s *stats) reportIfDue() {
	if time.Since(s.lastReport) < s.interval {
		return
	}

	now := time.Now()

	// Writer statistics
	if srtconn, ok := s.writer.(srt.Conn); ok {
		stats := &srt.Statistics{}
		srtconn.Stats(stats)

		if s.hub != nil {
			writerMsg := statsMessage{
				Timestamp: now,
				Type:      "writer",
				Stats:     stats,
			}
			if jsonData, err := json.Marshal(writerMsg); err == nil {
				select {
				case s.hub.broadcast <- jsonData:
				default:
				}
			}
		}
	}

	// Reader statistics
	if srtconn, ok := s.reader.(srt.Conn); ok {
		stats := &srt.Statistics{}
		srtconn.Stats(stats)

		if s.hub != nil {
			readerMsg := statsMessage{
				Timestamp: now,
				Type:      "reader",
				Stats:     stats,
				Links:     srtlaLinks(),
			}
			if jsonData, err := json.Marshal(readerMsg); err == nil {
				select {
				case s.hub.broadcast <- jsonData:
				default:
				}
			}
		}
	}

	s.lastReport = now
}

func handleWebSocket(hub *hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	hub.register <- conn

	go func() {
		defer func() {
			hub.unregister <- conn
		}()

		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()
}

// runSrtProxy forwards the SRT stream at from to the UDP address to. When
// wsPort is set, statistics are broadcast over WebSocket and passed to
// onStats. Startup errors are returned; after that the proxy keeps running,
// reconnecting the SRT reader and dropping packets the UDP output rejects.
// Write results are recorded in out (if non-nil) instead of being logged.
func runSrtProxy(from string, to string, wsPort int, telemetryFrom string, onStats func([]byte), out *udpOutput) error {
	var hub *hub
	if wsPort > 0 {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", wsPort))
		if err != nil {
			return fmt.Errorf("failed to start WebSocket server: %w", err)
		}

		hub = newHub(onStats)
		go hub.run()

		wsMux := http.NewServeMux()
		wsMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			handleWebSocket(hub, w, r)
		})

		log.Printf("WebSocket server address: ws://127.0.0.1:%d/ws", wsPort)
		go func() {
			if err := http.Serve(ln, wsMux); err != nil {
				log.Printf("WebSocket server error: %v", err)
			}
		}()
	}
	useRemoteStats := hub != nil && telemetryFrom != ""
	if useRemoteStats {
		go runStatsTelemetry(telemetryFrom, hub)
	}

	w, err := openUDPWriter(to)
	if err != nil {
		return fmt.Errorf("to: %w", err)
	}

	go func() {
		defer w.Close()

		buffer := make([]byte, 2048)

		s := &stats{
			interval: time.Second,
			writer:   w,
			hub:      hub,
		}

		for {
			r, err := openSrtStream(from)
			if err != nil {
				log.Printf("Failed to connect SRT reader: %v. Retrying in 5 seconds...", err)
				time.Sleep(5 * time.Second)
				continue
			}
			log.Println("SRT reader connected.")
			if !useRemoteStats {
				s.reader = r
			}

			for {
				n, err := r.Read(buffer)
				if err != nil {
					log.Printf("SRT reader error: %v. Attempting to reconnect...", err)
					r.Close()
					if !useRemoteStats {
						s.reader = nil
					}
					break
				}

				// UDP output is best effort: while nothing listens on the
				// port (e.g. the player is not running yet) writes fail with
				// "connection refused", so drop the packet and carry on.
				// The state is shown in the TUI rather than logged.
				_, err = w.Write(buffer[:n])
				out.record(time.Now(), err)
				s.reportIfDue()
			}
		}
	}()

	return nil
}

// udpOutput tracks the results of writes to the UDP downstream.
// A nil *udpOutput ignores records.
type udpOutput struct {
	mu   sync.Mutex
	snap udpOutputSnapshot
}

type udpOutputSnapshot struct {
	LastOK  time.Time // last successful write
	LastErr time.Time // last failed write
	Err     string    // error of the last failed write
}

// udpOutputErrorHold is how long a write error keeps the output marked as
// failing. With nothing listening, writes alternate between success and
// "connection refused" (the ICMP error surfaces on the next write), so a single
// successful write does not mean the output recovered.
const udpOutputErrorHold = 2 * time.Second

func (o *udpOutput) record(at time.Time, err error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		o.snap.LastErr = at
		o.snap.Err = err.Error()
	} else {
		o.snap.LastOK = at
	}
}

func (o *udpOutput) snapshot() udpOutputSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.snap
}

// srtPeerIdleTimeout is how long an SRT connection survives without hearing
// from the peer. gosrt's default (2s) drops the stream on brief outages that
// mobile senders (libsrt default: 5s) ride out, forcing a full reconnect.
const srtPeerIdleTimeout = 5 * time.Second

// newSRTConfig returns gosrt's default config with go-irl's overrides.
func newSRTConfig() srt.Config {
	config := srt.DefaultConfig()
	config.PeerIdleTimeout = srtPeerIdleTimeout
	return config
}

func openSrtStream(addr string) (io.ReadCloser, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	config := newSRTConfig()
	if err := config.UnmarshalQuery(u.RawQuery); err != nil {
		return nil, err
	}

	if u.Query().Get("mode") == "caller" {
		return srt.Dial("srt", u.Host, config)
	}

	ln, err := srt.Listen("srt", u.Host, config)
	if err != nil {
		return nil, err
	}

	conn, _, err := ln.Accept(func(req srt.ConnRequest) srt.ConnType {
		if len(config.StreamId) > 0 && config.StreamId != req.StreamId() {
			return srt.REJECT
		}

		req.SetPassphrase(config.Passphrase)

		return srt.PUBLISH
	})
	if err != nil {
		ln.Close()
		return nil, err
	}

	if conn == nil {
		ln.Close()
		return nil, fmt.Errorf("incoming connection rejected")
	}

	return listenerConn{Conn: conn, listener: ln}, nil
}

func openUDPWriter(addr string) (io.WriteCloser, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	raddr, err := net.ResolveUDPAddr("udp", u.Host)
	if err != nil {
		return nil, err
	}

	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, err
	}

	return conn, nil
}
