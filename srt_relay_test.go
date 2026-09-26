package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"
)

func TestMakeSRTURL(t *testing.T) {
	got := makeSRTURL("2001:db8::1", 5001, "caller", "a passphrase", downstreamStreamID)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	if u.Host != "[2001:db8::1]:5001" {
		t.Fatalf("unexpected host: %q", u.Host)
	}
	if value := u.Query().Get("mode"); value != "caller" {
		t.Fatalf("unexpected mode: %q", value)
	}
	if value := u.Query().Get("passphrase"); value != "a passphrase" {
		t.Fatalf("unexpected passphrase: %q", value)
	}
	if value := u.Query().Get("streamid"); value != downstreamStreamID {
		t.Fatalf("unexpected stream ID: %q", value)
	}
}

func TestSRTRelaySendsPublisherDataToCallingClient(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("get free port: %v", err)
	}

	const passphrase = "test-passphrase"
	relay := newSRTRelay(passphrase)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	server, err := relay.newServer(addr)
	if err != nil {
		t.Fatalf("start relay: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve()
	}()
	defer func() {
		server.Shutdown()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("relay server did not stop")
		}
	}()

	clientConfig := srt.DefaultConfig()
	clientConfig.StreamId = downstreamStreamID
	clientConfig.Passphrase = passphrase
	client, err := srt.Dial("srt", addr, clientConfig)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.Close()

	publisherConfig := srt.DefaultConfig()
	publisherConfig.Passphrase = passphrase
	publisher, err := srt.Dial("srt", addr, publisherConfig)
	if err != nil {
		t.Fatalf("dial publisher: %v", err)
	}
	defer publisher.Close()

	payload := bytes.Repeat([]byte{0x47}, 7*188)
	if _, err := publisher.Write(payload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(client, received); err != nil {
		t.Fatalf("read relayed payload: %v", err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("relayed payload does not match publisher data")
	}
}

func TestSRTRelaySendsUpstreamStatisticsToCallingClient(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("get free port: %v", err)
	}

	const passphrase = "test-passphrase"
	relay := newSRTRelay(passphrase)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	server, err := relay.newServer(addr)
	if err != nil {
		t.Fatalf("start relay: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve()
	}()
	defer func() {
		server.Shutdown()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("relay server did not stop")
		}
	}()

	statsConfig := srt.DefaultConfig()
	statsConfig.StreamId = downstreamStatsStreamID
	statsConfig.Passphrase = passphrase
	statsClient, err := srt.Dial("srt", addr, statsConfig)
	if err != nil {
		t.Fatalf("dial statistics client: %v", err)
	}
	defer statsClient.Close()

	publisherConfig := srt.DefaultConfig()
	publisherConfig.Passphrase = passphrase
	publisher, err := srt.Dial("srt", addr, publisherConfig)
	if err != nil {
		t.Fatalf("dial publisher: %v", err)
	}
	defer publisher.Close()

	payload := bytes.Repeat([]byte{0x47}, 7*188)
	if _, err := publisher.Write(payload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	result := make(chan statsMessage, 1)
	errResult := make(chan error, 1)
	go func() {
		for {
			payload, err := readTelemetryFrame(statsClient)
			if err != nil {
				errResult <- err
				return
			}
			var message statsMessage
			if err := json.Unmarshal(payload, &message); err != nil || message.Type != "reader" {
				continue
			}
			result <- message
			return
		}
	}()

	select {
	case message := <-result:
		if message.Stats == nil {
			t.Fatal("statistics payload is nil")
		}
		if message.Stats.Accumulated.PktRecv == 0 {
			t.Fatal("upstream statistics did not include received packets")
		}
		snap := relay.snapshot()
		if snap.PublisherAddr == "" || snap.Stats == nil {
			t.Fatalf("snapshot has no publisher: %+v", snap)
		}
		if snap.StatsClients != 1 || snap.Subscribers != 0 {
			t.Fatalf("snapshot clients = %d stream / %d stats, want 0 / 1", snap.Subscribers, snap.StatsClients)
		}
	case err := <-errResult:
		t.Fatalf("read statistics: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream statistics")
	}
}

func TestForwardTelemetryPayloadIgnoresHeartbeat(t *testing.T) {
	h := &hub{broadcast: make(chan []byte, 1)}
	if forwardTelemetryPayload(h, telemetryHeartbeat) {
		t.Fatal("heartbeat was forwarded to browser clients")
	}

	payload := []byte(`{"type":"reader","stats":{}}`)
	if !forwardTelemetryPayload(h, payload) {
		t.Fatal("reader statistics were not forwarded")
	}
	if got := <-h.broadcast; !bytes.Equal(got, payload) {
		t.Fatal("forwarded telemetry payload changed")
	}
}
