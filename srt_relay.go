package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	srt "github.com/datarhei/gosrt"
)

const (
	downstreamStreamID      = "go-irl-client"
	downstreamStatsStreamID = "go-irl-stats"
)

// srtRelay accepts the SRT stream reconstructed by the local SRTLA server as
// its single publisher and forwards it to clients that connect as subscribers.
// A new pub/sub generation is created whenever the publisher disconnects so a
// mobile stream can reconnect without restarting the process.
type srtRelay struct {
	passphrase string

	mu              sync.Mutex
	channel         srt.PubSub
	publisherActive bool
	publisher       srt.Conn
	publisherGen    uint64
	subscribers     int
	statsClients    int
}

type relaySnapshot struct {
	PublisherAddr string // empty when no publisher is connected
	Stats         *srt.Statistics
	Subscribers   int
	StatsClients  int
}

// snapshot reports the relay's current publisher and client counts.
func (r *srtRelay) snapshot() relaySnapshot {
	r.mu.Lock()
	snap := relaySnapshot{Subscribers: r.subscribers, StatsClients: r.statsClients}
	publisher := r.publisher
	r.mu.Unlock()

	if publisher != nil {
		snap.PublisherAddr = publisher.RemoteAddr().String()
		snap.Stats = &srt.Statistics{}
		publisher.Stats(snap.Stats)
	}
	return snap
}

func (r *srtRelay) addClients(counter *int, delta int) {
	r.mu.Lock()
	*counter += delta
	r.mu.Unlock()
}

func newSRTRelay(passphrase string) *srtRelay {
	return &srtRelay{
		passphrase: passphrase,
		channel:    srt.NewPubSub(srt.PubSubConfig{}),
	}
}

func (r *srtRelay) handleConnect(req srt.ConnRequest) srt.ConnType {
	mode := srt.REJECT
	if req.StreamId() == downstreamStreamID || req.StreamId() == downstreamStatsStreamID {
		mode = srt.SUBSCRIBE
	} else if isLoopbackAddr(req.RemoteAddr()) {
		// The SRTLA component always forwards its reconstructed SRT packets to
		// this listener through 127.0.0.1. Do not allow a public connection to
		// become the publisher.
		mode = srt.PUBLISH
	}

	if mode == srt.REJECT || !r.authorize(req) {
		log.Printf("Rejected SRT connection from %s", req.RemoteAddr())
		return srt.REJECT
	}

	return mode
}

func (r *srtRelay) authorize(req srt.ConnRequest) bool {
	if r.passphrase == "" {
		return !req.IsEncrypted()
	}
	if !req.IsEncrypted() {
		return false
	}
	return req.SetPassphrase(r.passphrase) == nil
}

func (r *srtRelay) handlePublish(conn srt.Conn) {
	r.mu.Lock()
	if r.publisherActive {
		r.mu.Unlock()
		log.Printf("Rejected additional SRT publisher from %s", conn.RemoteAddr())
		_ = conn.Close()
		return
	}
	channel := r.channel
	r.publisherActive = true
	r.publisher = conn
	r.publisherGen++
	r.mu.Unlock()

	log.Printf("SRTLA stream connected to relay from %s", conn.RemoteAddr())
	err := channel.Publish(conn)
	_ = conn.Close()
	if err != nil {
		log.Printf("SRTLA stream disconnected from relay: %v", err)
	}

	r.mu.Lock()
	if r.channel == channel {
		r.channel = srt.NewPubSub(srt.PubSubConfig{})
		r.publisherActive = false
		if r.publisher == conn {
			r.publisher = nil
			r.publisherGen++
		}
	}
	r.mu.Unlock()
}

func (r *srtRelay) handleSubscribe(conn srt.Conn) {
	if conn.StreamId() == downstreamStatsStreamID {
		r.handleStatsSubscribe(conn)
		return
	}

	r.mu.Lock()
	channel := r.channel
	r.mu.Unlock()

	log.Printf("Downstream client connected from %s", conn.RemoteAddr())
	r.addClients(&r.subscribers, 1)
	defer r.addClients(&r.subscribers, -1)
	err := channel.Subscribe(conn)
	_ = conn.Close()
	if err != nil {
		log.Printf("Downstream client disconnected: %v", err)
	}
}

func (r *srtRelay) handleStatsSubscribe(conn srt.Conn) {
	log.Printf("Statistics client connected from %s", conn.RemoteAddr())
	r.addClients(&r.statsClients, 1)
	defer r.addClients(&r.statsClients, -1)
	defer func() {
		_ = conn.Close()
		log.Printf("Statistics client disconnected from %s", conn.RemoteAddr())
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var generation uint64
	statistics := &srt.Statistics{}

	for {
		<-ticker.C

		r.mu.Lock()
		publisher := r.publisher
		publisherGen := r.publisherGen
		r.mu.Unlock()

		var payload []byte
		if publisher == nil {
			payload = telemetryHeartbeat
		} else {
			if publisherGen != generation {
				statistics = &srt.Statistics{}
				generation = publisherGen
			}
			publisher.Stats(statistics)
			if statistics.Interval.MsInterval == 0 {
				continue
			}
			message := statsMessage{
				Timestamp: time.Now(),
				Type:      "reader",
				Stats:     statistics,
			}
			var err error
			payload, err = json.Marshal(message)
			if err != nil {
				log.Printf("Failed to encode upstream SRT statistics: %v", err)
				return
			}
		}

		if err := writeTelemetryFrame(conn, payload); err != nil {
			log.Printf("Failed to send upstream SRT statistics: %v", err)
			return
		}
	}
}

func (r *srtRelay) newServer(addr string) (*srt.Server, error) {
	config := srt.DefaultConfig()
	server := &srt.Server{
		Addr:            addr,
		Config:          &config,
		HandleConnect:   r.handleConnect,
		HandlePublish:   r.handlePublish,
		HandleSubscribe: r.handleSubscribe,
	}
	if err := server.Listen(); err != nil {
		return nil, fmt.Errorf("listen for downstream SRT: %w", err)
	}
	return server, nil
}

func isLoopbackAddr(addr net.Addr) bool {
	udpAddr, ok := addr.(*net.UDPAddr)
	return ok && udpAddr.IP.IsLoopback()
}
