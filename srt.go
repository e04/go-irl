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
}

func newHub() *hub {
	return &hub{
		clients:    make(map[*websocket.Conn]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
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

func runSrtProxy(from string, to string, wsPort int) <-chan error {
	var hub *hub
	if wsPort > 0 {
		hub = newHub()
		go hub.run()

		wsMux := http.NewServeMux()
		wsMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			handleWebSocket(hub, w, r)
		})

		go func() {
			log.Printf("WebSocket server address: ws://127.0.0.1:%d/ws", wsPort)
			if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", wsPort), wsMux); err != nil {
				log.Printf("WebSocket server error: %v", err)
			}
		}()
	}

	doneChan := make(chan error, 1)

	r, err := openSrtStream(from)
	if err != nil {
		doneChan <- fmt.Errorf("from: %w", err)
		return doneChan
	}

	w, err := openUDPWriter(to)
	if err != nil {
		r.Close()
		doneChan <- fmt.Errorf("to: %w", err)
		return doneChan
	}

	go func() {
		defer r.Close()
		defer w.Close()

		buffer := make([]byte, 2048)

		s := &stats{
			interval: time.Second,
			reader:   r,
			writer:   w,
			hub:      hub,
		}

		for {
			n, err := r.Read(buffer)
			if err != nil {
				log.Printf("\nSRT reader error: %v. Attempting to reconnect...", err)
				r.Close()
				for {
					var reconnErr error
					r, reconnErr = openSrtStream(from)
					if reconnErr == nil {
						log.Println("SRT reader reconnected successfully.")
						s.reader = r
						break
					}
					log.Printf("Failed to reconnect reader: %v. Retrying in 5 seconds...", reconnErr)
					time.Sleep(5 * time.Second)
				}
				continue
			}

			if _, err := w.Write(buffer[:n]); err != nil {
				doneChan <- fmt.Errorf("write: %w", err)
				return
			}
			s.reportIfDue()
		}
	}()

	return doneChan
}

func openSrtStream(addr string) (io.ReadCloser, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	config := srt.DefaultConfig()
	if err := config.UnmarshalQuery(u.RawQuery); err != nil {
		return nil, err
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
