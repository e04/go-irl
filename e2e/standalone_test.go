//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const passphrase = "e2e-passphrase"

var threeLinks = []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"}

// streamSetup is the standalone pipeline with a bonded sender attached.
type streamSetup struct {
	irl    *standalone
	proxy  *linkProxy
	sender *sender
	sink   *udpSink
	ws     *wsClient
}

func setupStandalone(t *testing.T, links ...string) *streamSetup {
	t.Helper()
	s := &streamSetup{}
	s.irl = startStandalone(t, passphrase)
	s.sink = startUDPSink(t, s.irl.udpPort)
	s.ws = connectWS(t, s.irl.wsPort)
	s.proxy = startLinkProxy(t, s.irl.srtlaPort)
	s.sender = startSender(t, s.proxy.port(), links...)
	s.irl.waitLog(t, logGroupRegistered, 1, 5*time.Second)
	s.irl.waitLog(t, logConnRegistered, len(links), 5*time.Second)
	return s
}

func TestStandaloneBonding(t *testing.T) {
	requireE2E(t)
	s := setupStandalone(t, threeLinks...)

	pub := startPublisher(t, dialPublisher(t, s.sender.listenPort, passphrase, 20*time.Second), 1, 4)
	s.irl.waitLog(t, `SRT reader connected`, 1, 10*time.Second)
	readers := s.ws.waitReaders(t, 0, 4, 15*time.Second, "reader statistics with traffic", func(m statsMessage) bool {
		return m.Stats.Accumulated.PktRecv > 0
	})
	pub.pause()
	sent := pub.sentCount()
	s.sink.requireIntact(t, 1, sent)

	t.Run("all links carry data", func(t *testing.T) {
		perLink := s.proxy.dataPackets()
		for _, ip := range threeLinks {
			if perLink[ip] == 0 {
				t.Errorf("link %s carried no SRT data (per link: %v)", ip, perLink)
			}
		}
	})

	t.Run("statistics", func(t *testing.T) {
		s.ws.requireWellFormed(t)
		last := readers[len(readers)-1]
		st := last.Stats
		if st.Accumulated.PktRecv == 0 || st.Accumulated.ByteRecv == 0 {
			t.Errorf("no received packets in stats: %+v", st.Accumulated)
		}
		if st.Instantaneous.MbpsRecvRate <= 0 {
			t.Errorf("Instantaneous.MbpsRecvRate = %v, want > 0", st.Instantaneous.MbpsRecvRate)
		}
		if r := st.Interval.MbpsRecvRate; r < 1 || r > 10 {
			t.Errorf("Interval.MbpsRecvRate = %.2f, want about 4", r)
		}
		if rtt := st.Instantaneous.MsRTT; rtt <= 0 || rtt > 200 {
			t.Errorf("MsRTT = %v on loopback", rtt)
		}
		if loss := st.Instantaneous.PktRecvLossRate; loss != 0 {
			t.Errorf("PktRecvLossRate = %v without loss", loss)
		}
		for i := 1; i < len(readers); i++ {
			gap := readers[i].Timestamp.Sub(readers[i-1].Timestamp)
			if gap < 500*time.Millisecond || gap > 3*time.Second {
				t.Errorf("stats %d arrived %v after the previous one, want about 1s", i, gap)
			}
			if readers[i].Stats.Accumulated.PktRecv < readers[i-1].Stats.Accumulated.PktRecv {
				t.Errorf("Accumulated.PktRecv went backwards at %d", i)
			}
		}
		s.ws.dump(t, map[string]any{"quality": "good", "disconnected": false})
	})

	t.Run("browser source", func(t *testing.T) {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(s.irl.bsPort) + "/app")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `<div id="root">`) {
			t.Fatalf("GET /app = %d, body %.200q", resp.StatusCode, body)
		}
	})
}

// Phones connect before going live. With no SRT traffic, only keepalive
// echoes keep the links up (srtla_send gives a link up after 4s of silence).
func TestStandaloneIdleLinksStayUp(t *testing.T) {
	requireE2E(t)
	s := setupStandalone(t, threeLinks...)
	time.Sleep(10 * time.Second)
	if n := s.sender.logCount(`connection failed`); n > 0 {
		t.Fatalf("srtla_send dropped %d idle link(s)", n)
	}
	if n := s.irl.logCount(logConnRegistered); n != len(threeLinks) {
		t.Fatalf("%d link registrations while idle, want %d", n, len(threeLinks))
	}

	pub := startPublisher(t, dialPublisher(t, s.sender.listenPort, passphrase, 10*time.Second), 1, 4)
	eventually(t, 10*time.Second, "output to flow", func() bool { return s.sink.received(1) > 200 })
	pub.pause()
	s.sink.requireIntact(t, 1, pub.sentCount())
}

// A link that goes dark mid-stream must not cost any data: srtla_send stops
// using it, SRT retransmits over the others, go-irl expires it, and it
// rejoins the group once it comes back.
func TestStandaloneLinkFailover(t *testing.T) {
	requireE2E(t)
	s := setupStandalone(t, threeLinks...)
	pub := startPublisher(t, dialPublisher(t, s.sender.listenPort, passphrase, 20*time.Second), 1, 4)
	eventually(t, 10*time.Second, "output to flow", func() bool { return s.sink.received(1) > 200 })

	const dead = "127.0.0.2"
	s.proxy.setLoss(dead, 1)
	s.sender.waitLog(t, dead+`.*connection failed`, 1, 10*time.Second)
	s.irl.waitLog(t, logConnTimedOut, 1, 25*time.Second)
	if n := s.irl.logCount(logConnTimedOut); n != 1 {
		t.Fatalf("%d connections timed out, want only the dead one", n)
	}

	s.proxy.setLoss(dead, 0)
	s.irl.waitLog(t, logConnRegistered, len(threeLinks)+1, 15*time.Second)
	before := s.proxy.dataPackets()[dead]
	eventually(t, 15*time.Second, "restored link to carry data", func() bool {
		return s.proxy.dataPackets()[dead] > before
	})

	pub.pause()
	s.sink.requireIntact(t, 1, pub.sentCount())
	if n := s.irl.logCount(logGroupRegistered); n != 1 {
		t.Fatalf("%d groups registered, want the original one only", n)
	}
}

// Sustained loss on every link must show up in the statistics the Browser
// Source uses for scene switching, and clear once the links recover.
func TestStandaloneLossReflectedInStats(t *testing.T) {
	requireE2E(t)
	links := []string{"127.0.0.1", "127.0.0.2"}
	s := setupStandalone(t, links...)
	pub := startPublisher(t, dialPublisher(t, s.sender.listenPort, passphrase, 20*time.Second), 1, 2)
	s.ws.waitReaders(t, 0, 2, 10*time.Second, "baseline statistics", func(m statsMessage) bool {
		return m.Stats.Accumulated.PktRecv > 0
	})

	mark := s.ws.mark()
	for _, ip := range links {
		s.proxy.setLoss(ip, 0.3)
	}
	lossy := s.ws.waitReaders(t, mark, 3, 20*time.Second, "statistics reporting high loss", func(m statsMessage) bool {
		return m.Stats.Instantaneous.PktRecvLossRate >= 20
	})
	if last := lossy[len(lossy)-1].Stats; last.Accumulated.PktRecvLoss == 0 || last.Accumulated.PktRecvRetrans == 0 {
		t.Errorf("loss not accounted: PktRecvLoss=%d PktRecvRetrans=%d", last.Accumulated.PktRecvLoss, last.Accumulated.PktRecvRetrans)
	}

	mark = s.ws.mark()
	for _, ip := range links {
		s.proxy.setLoss(ip, 0)
	}
	s.ws.waitReaders(t, mark, 3, 20*time.Second, "statistics reporting recovery", func(m statsMessage) bool {
		return m.Stats.Instantaneous.PktRecvLossRate < 5
	})
	received := s.sink.received(1)
	eventually(t, 5*time.Second, "output to keep flowing", func() bool { return s.sink.received(1) > received+100 })
	pub.pause()

	s.ws.requireWellFormed(t)
	s.ws.dump(t, map[string]any{"quality": []string{"good", "poor", "good"}, "disconnected": false})
}

// When the phone drops off entirely, statistics stop so the Browser Source
// switches to the offline scene, and a new session is picked up without
// restarting go-irl.
func TestStandaloneSenderReconnect(t *testing.T) {
	requireE2E(t)
	s := setupStandalone(t, threeLinks...)
	pub := startPublisher(t, dialPublisher(t, s.sender.listenPort, passphrase, 20*time.Second), 1, 4)
	s.ws.waitReaders(t, 0, 2, 10*time.Second, "statistics", func(m statsMessage) bool {
		return m.Stats.Accumulated.PktRecv > 0
	})

	pub.pause()
	s.sink.requireIntact(t, 1, pub.sentCount())
	s.sender.kill()
	pub.close()
	s.irl.waitLog(t, `SRT reader error`, 1, 10*time.Second)
	mark := s.ws.mark()
	time.Sleep(6 * time.Second)
	if got := s.ws.readersSince(mark); len(got) > 0 {
		t.Fatalf("%d statistics messages while no stream was connected", len(got))
	}

	sender2 := startSender(t, s.proxy.port(), threeLinks...)
	s.irl.waitLog(t, logGroupRegistered, 2, 5*time.Second)
	pub2 := startPublisher(t, dialPublisher(t, sender2.listenPort, passphrase, 30*time.Second), 2, 4)
	s.irl.waitLog(t, `SRT reader connected`, 2, 10*time.Second)
	// The Browser Source derives rates from consecutive messages of the same
	// connection, so it needs a baseline plus three samples to report "good".
	resumed := s.ws.waitReaders(t, mark, 4, 15*time.Second, "statistics after reconnect", func(m statsMessage) bool {
		return m.Stats.Accumulated.PktRecv > 0
	})
	pub2.pause()
	s.sink.requireIntact(t, 2, pub2.sentCount())
	if resumed[0].Stats.Accumulated.PktRecv > pub2.sentCount()+1000 {
		t.Errorf("statistics carried over from the previous session: PktRecv=%d", resumed[0].Stats.Accumulated.PktRecv)
	}
	s.ws.dump(t, map[string]any{"quality": "good", "disconnected": false})
}
