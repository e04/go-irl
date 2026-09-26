//go:build e2e

package e2e

import (
	"strconv"
	"testing"
	"time"
)

type relaySetup struct {
	server  *process
	proxy   *linkProxy
	sender  *sender
	client  *goIRLClient
	srtPort int
}

type goIRLClient struct {
	*process
	sink *udpSink
	ws   *wsClient
}

func startServer(t *testing.T) (*process, int, int) {
	t.Helper()
	srtlaPort, srtPort := freeUDPPort(t), freeUDPPort(t)
	p := goIRL(t, "go-irl server",
		"-mode", "server",
		"-srtla-port", strconv.Itoa(srtlaPort),
		"-srt-port", strconv.Itoa(srtPort),
		"-passphrase", passphrase)
	p.waitLog(t, `Listening on`, 1, 10*time.Second)
	return p, srtlaPort, srtPort
}

func startClient(t *testing.T, name string, srtPort int, pass string) *goIRLClient {
	t.Helper()
	wsPort, udpPort := freeTCPPort(t), freeUDPPort(t)
	c := &goIRLClient{sink: startUDPSink(t, udpPort)}
	c.process = goIRL(t, name,
		"-mode", "client",
		"-srt-host", "127.0.0.1",
		"-srt-port", strconv.Itoa(srtPort),
		"-ws-port", strconv.Itoa(wsPort),
		"-bs-port", strconv.Itoa(freeTCPPort(t)),
		"-udp-port", strconv.Itoa(udpPort),
		"-passphrase", pass)
	c.waitLog(t, `WebSocket server address`, 1, 10*time.Second)
	c.ws = connectWS(t, wsPort)
	return c
}

func setupRelay(t *testing.T) *relaySetup {
	t.Helper()
	r := &relaySetup{}
	var srtlaPort int
	r.server, srtlaPort, r.srtPort = startServer(t)
	r.client = startClient(t, "go-irl client", r.srtPort, passphrase)
	r.client.waitLog(t, `SRT reader connected`, 1, 10*time.Second)
	r.client.waitLog(t, `Upstream statistics channel connected`, 1, 10*time.Second)
	r.proxy = startLinkProxy(t, srtlaPort)
	r.sender = startSender(t, r.proxy.port(), threeLinks...)
	r.server.waitLog(t, logConnRegistered, len(threeLinks), 5*time.Second)
	return r
}

// Server mode on a VPS, client mode next to OBS: the bonded stream and the
// upstream leg's statistics both have to cross the relay.
func TestServerClientRelay(t *testing.T) {
	requireE2E(t)
	r := setupRelay(t)
	intruder := startClient(t, "go-irl client (wrong passphrase)", r.srtPort, "not-the-passphrase")

	t.Run("publisher with wrong passphrase is rejected", func(t *testing.T) {
		rejected := r.server.logCount(`Rejected SRT connection`)
		if conn, err := dialOnce(r.sender.listenPort, "wrong-passphrase"); err == nil {
			conn.Close()
			t.Fatal("relay accepted a publisher with the wrong passphrase")
		}
		if conn, err := dialOnce(r.sender.listenPort, ""); err == nil {
			conn.Close()
			t.Fatal("relay accepted an unencrypted publisher")
		}
		r.server.waitLog(t, `Rejected SRT connection`, rejected+2, 5*time.Second)
	})

	pub := startPublisher(t, dialPublisher(t, r.sender.listenPort, passphrase, 20*time.Second), 1, 4)
	r.server.waitLog(t, `SRTLA stream connected to relay`, 1, 10*time.Second)
	readers := r.client.ws.waitReaders(t, 0, 3, 15*time.Second, "upstream statistics at the client", func(m statsMessage) bool {
		return m.Stats.Accumulated.PktRecv > 0
	})

	t.Run("second publisher cannot hijack the stream", func(t *testing.T) {
		other := startSender(t, r.proxy.port(), "127.0.0.4")
		if conn, err := dialOnce(other.listenPort, passphrase); err == nil {
			defer conn.Close()
		}
		r.server.waitLog(t, `Rejected additional SRT publisher`, 1, 10*time.Second)
	})

	received := r.client.sink.received(1)
	eventually(t, 5*time.Second, "stream to keep flowing", func() bool { return r.client.sink.received(1) > received+100 })
	pub.pause()
	r.client.sink.requireIntact(t, 1, pub.sentCount())

	t.Run("statistics", func(t *testing.T) {
		r.client.ws.requireWellFormed(t)
		for _, m := range r.client.ws.messages() {
			if m.Type != "reader" {
				t.Fatalf("client forwarded a %q message; only the upstream reader stats are meaningful", m.Type)
			}
		}
		st := readers[len(readers)-1].Stats
		if st.Interval.MbpsRecvRate < 1 || st.Interval.MbpsRecvRate > 10 {
			t.Errorf("upstream Interval.MbpsRecvRate = %.2f, want about 4", st.Interval.MbpsRecvRate)
		}
		if st.Accumulated.PktRecv < uint64(r.client.sink.received(1))/2 {
			t.Errorf("upstream PktRecv = %d but client received %d messages", st.Accumulated.PktRecv, r.client.sink.received(1))
		}
		r.client.ws.dump(t, map[string]any{"quality": "good", "disconnected": false})
	})

	t.Run("client with wrong passphrase gets nothing", func(t *testing.T) {
		if n := intruder.sink.received(1); n > 0 {
			t.Fatalf("intruder received %d messages", n)
		}
		if n := len(intruder.ws.messages()); n > 0 {
			t.Fatalf("intruder received %d statistics messages", n)
		}
		if intruder.logCount(`SRT reader connected`) > 0 {
			t.Fatal("intruder connected to the relay")
		}
	})
}

// A phone that reconnects (new SRTLA group, new SRT session) must reach the
// client again without restarting either side.
func TestServerPublisherReconnect(t *testing.T) {
	requireE2E(t)
	r := setupRelay(t)
	pub := startPublisher(t, dialPublisher(t, r.sender.listenPort, passphrase, 20*time.Second), 1, 4)
	eventually(t, 10*time.Second, "stream to reach the client", func() bool { return r.client.sink.received(1) > 200 })

	pub.pause()
	r.client.sink.requireIntact(t, 1, pub.sentCount())
	r.sender.kill()
	pub.close()
	r.server.waitLog(t, `SRTLA stream disconnected from relay`, 1, 10*time.Second)

	sender2 := startSender(t, r.proxy.port(), threeLinks...)
	r.server.waitLog(t, logGroupRegistered, 2, 5*time.Second)
	mark := r.client.ws.mark()
	pub2 := startPublisher(t, dialPublisher(t, sender2.listenPort, passphrase, 30*time.Second), 2, 4)
	r.server.waitLog(t, `SRTLA stream connected to relay`, 2, 10*time.Second)
	eventually(t, 20*time.Second, "new session to reach the client", func() bool { return r.client.sink.received(2) > 200 })
	r.client.ws.waitReaders(t, mark, 2, 15*time.Second, "statistics for the new session", func(m statsMessage) bool {
		return m.Stats.Accumulated.PktRecv > 0
	})
	pub2.pause()
	r.client.sink.requireIntact(t, 2, pub2.sentCount())
}
