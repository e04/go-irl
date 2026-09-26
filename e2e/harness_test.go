//go:build e2e

// Package e2e drives the real go-irl binary with the irlserver/srtla sender
// (srtla_send). Traffic flows
//
//	SRT caller (gosrt) → srtla_send → linkProxy → go-irl → UDP sink / WebSocket
//
// linkProxy sits between the sender's bonded links and go-irl so tests can
// drop packets per link. Each link binds a distinct loopback address
// (127.0.0.x), which Linux routes without configuration; srtla_send is
// Linux-only anyway. Run with SRTLA_SEND pointing at the sender binary, or
// use e2e/run-docker.sh.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"
	"github.com/gorilla/websocket"
)

var (
	goIRLBin   string
	srtlaSend  string
	skipReason string
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	srtlaSend = os.Getenv("SRTLA_SEND")
	if srtlaSend == "" {
		skipReason = "SRTLA_SEND is not set (see e2e/run-docker.sh)"
		return m.Run()
	}

	dir, err := os.MkdirTemp("", "go-irl-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)

	goIRLBin = filepath.Join(dir, "go-irl")
	args := []string{"build", "-o", goIRLBin}
	if os.Getenv("E2E_RACE") != "" {
		args = append(args, "-race")
	}
	build := exec.Command("go", append(args, "..")...)
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build go-irl: %v\n", err)
		return 1
	}
	return m.Run()
}

func requireE2E(t *testing.T) {
	t.Helper()
	if skipReason != "" {
		t.Skip(skipReason)
	}
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// logBuffer collects a process's output line by line.
type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (b *logBuffer) add(line string) {
	b.mu.Lock()
	b.lines = append(b.lines, line)
	b.mu.Unlock()
}

func (b *logBuffer) count(re *regexp.Regexp) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, l := range b.lines {
		if re.MatchString(l) {
			n++
		}
	}
	return n
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

type process struct {
	name string
	cmd  *exec.Cmd
	logs logBuffer
	done chan struct{}
}

// startProcess runs bin until the test ends. Its output is dumped when the
// test fails, and a data race report from a -race build fails the test.
func startProcess(t *testing.T, name, bin string, args ...string) *process {
	t.Helper()
	p := &process{name: name, cmd: exec.Command(bin, args...), done: make(chan struct{})}
	pr, pw := io.Pipe()
	p.cmd.Stdout, p.cmd.Stderr = pw, pw
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	go func() {
		s := bufio.NewScanner(pr)
		for s.Scan() {
			p.logs.add(s.Text())
		}
	}()
	go func() {
		_ = p.cmd.Wait()
		pw.Close()
		close(p.done)
	}()
	t.Cleanup(func() {
		p.stop()
		if t.Failed() {
			t.Logf("---- %s output ----\n%s", name, p.logs.String())
		}
		if p.logs.count(regexp.MustCompile(`DATA RACE`)) > 0 {
			t.Errorf("%s reported a data race:\n%s", name, p.logs.String())
		}
	})
	return p
}

func (p *process) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// kill simulates a crash: no shutdown packets are sent.
func (p *process) kill() {
	_ = p.cmd.Process.Kill()
	<-p.done
}

func (p *process) waitLog(t *testing.T, pattern string, n int, timeout time.Duration) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	eventually(t, timeout, fmt.Sprintf("%s to log %q %d time(s)", p.name, pattern, n), func() bool {
		return p.logs.count(re) >= n
	})
}

func (p *process) logCount(pattern string) int {
	return p.logs.count(regexp.MustCompile(pattern))
}

// Log lines emitted by go-irl's SRTLA receiver.
const (
	logGroupRegistered = `\] Registered$`
	logConnRegistered  = `Conn Registered$`
	logConnTimedOut    = `Connection removed \(timed out\)`
)

// goIRL starts go-irl in plain log mode and waits until it listens.
func goIRL(t *testing.T, name string, args ...string) *process {
	t.Helper()
	p := startProcess(t, name, goIRLBin, append([]string{"-cli"}, args...)...)
	return p
}

type standalone struct {
	*process
	srtlaPort, wsPort, bsPort, udpPort int
}

func startStandalone(t *testing.T, passphrase string) *standalone {
	t.Helper()
	s := &standalone{srtlaPort: freeUDPPort(t), wsPort: freeTCPPort(t), bsPort: freeTCPPort(t), udpPort: freeUDPPort(t)}
	args := []string{
		"-mode", "standalone",
		"-srtla-port", strconv.Itoa(s.srtlaPort),
		"-ws-port", strconv.Itoa(s.wsPort),
		"-bs-port", strconv.Itoa(s.bsPort),
		"-udp-port", strconv.Itoa(s.udpPort),
	}
	if passphrase != "" {
		args = append(args, "-passphrase", passphrase)
	}
	s.process = goIRL(t, "go-irl standalone", args...)
	s.waitLog(t, `WebSocket server address`, 1, 10*time.Second)
	return s
}

// linkProxy relays UDP between srtla_send's links and go-irl, keeping one
// upstream socket per link so go-irl still sees each link as a separate
// address. Packet loss can be injected per link source IP.
type linkProxy struct {
	t      *testing.T
	ln     *net.UDPConn
	target *net.UDPAddr

	mu      sync.Mutex
	links   map[string]*net.UDPConn // client addr → upstream socket
	loss    map[string]float64      // client IP → drop probability
	dataPkt map[string]int          // client IP → SRT data packets forwarded upstream
	rng     *rand.Rand
}

func startLinkProxy(t *testing.T, targetPort int) *linkProxy {
	t.Helper()
	ln, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := &linkProxy{
		t:       t,
		ln:      ln,
		target:  &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: targetPort},
		links:   map[string]*net.UDPConn{},
		loss:    map[string]float64{},
		dataPkt: map[string]int{},
		rng:     rand.New(rand.NewPCG(1, 2)),
	}
	go p.serve()
	t.Cleanup(func() {
		ln.Close()
		p.mu.Lock()
		for _, c := range p.links {
			c.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *linkProxy) port() int { return p.ln.LocalAddr().(*net.UDPAddr).Port }

// setLoss drops the given fraction of packets in both directions on the
// link bound to ip. 1 blackholes the link.
func (p *linkProxy) setLoss(ip string, loss float64) {
	p.mu.Lock()
	p.loss[ip] = loss
	p.mu.Unlock()
}

func (p *linkProxy) drop(ip string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	loss := p.loss[ip]
	return loss > 0 && p.rng.Float64() < loss
}

func (p *linkProxy) dataPackets() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for k, v := range p.dataPkt {
		out[k] = v
	}
	return out
}

func (p *linkProxy) serve() {
	buf := make([]byte, 2048)
	for {
		n, from, err := p.ln.ReadFromUDP(buf)
		if err != nil {
			return
		}
		ip := from.IP.String()
		if p.drop(ip) {
			continue
		}
		up := p.upstream(from)
		if up == nil {
			continue
		}
		if n >= 16 && buf[0]&0x80 == 0 {
			p.mu.Lock()
			p.dataPkt[ip]++
			p.mu.Unlock()
		}
		_, _ = up.Write(buf[:n])
	}
}

func (p *linkProxy) upstream(from *net.UDPAddr) *net.UDPConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.links[from.String()]; ok {
		return c
	}
	c, err := net.DialUDP("udp4", nil, p.target)
	if err != nil {
		return nil
	}
	p.links[from.String()] = c
	client := *from
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := c.Read(buf)
			if err != nil {
				if !isClosed(err) {
					// go-irl not listening yet (ICMP refused); keep reading.
					time.Sleep(10 * time.Millisecond)
					continue
				}
				return
			}
			if p.drop(client.IP.String()) {
				continue
			}
			_, _ = p.ln.WriteToUDP(buf[:n], &client)
		}
	}()
	return c
}

func isClosed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "use of closed network connection")
}

type sender struct {
	*process
	ipsFile    string
	listenPort int
}

// startSender runs srtla_send with one bonded link per source IP, all aimed
// at srtlaPort, and waits until every link is registered.
func startSender(t *testing.T, srtlaPort int, ips ...string) *sender {
	t.Helper()
	s := &sender{ipsFile: filepath.Join(t.TempDir(), "ips"), listenPort: freeUDPPort(t)}
	s.writeIPs(t, ips...)
	s.process = startProcess(t, "srtla_send", srtlaSend,
		strconv.Itoa(s.listenPort), "127.0.0.1", strconv.Itoa(srtlaPort), s.ipsFile)
	s.waitLog(t, `connection established`, len(ips), 15*time.Second)
	return s
}

func (s *sender) writeIPs(t *testing.T, ips ...string) {
	t.Helper()
	if err := os.WriteFile(s.ipsFile, []byte(strings.Join(ips, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setIPs changes the sender's links at runtime (SIGHUP reloads the file).
func (s *sender) setIPs(t *testing.T, ips ...string) {
	t.Helper()
	s.writeIPs(t, ips...)
	if err := s.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
}

// Payload layout: every UDP datagram go-irl outputs is one SRT message of 7
// MPEG-TS packets. The first carries a publisher epoch and sequence number.
const (
	tsPacketSize = 188
	messageSize  = 7 * tsPacketSize
)

type publisher struct {
	conn  srt.Conn
	epoch uint32
	stop  chan struct{}
	done  chan struct{}

	mu   sync.Mutex
	sent uint64
	err  error
}

func publisherConfig(passphrase string) srt.Config {
	cfg := srt.DefaultConfig()
	cfg.Passphrase = passphrase
	// Leave room for retransmissions over a lossy or dead link.
	cfg.ReceiverLatency = 500 * time.Millisecond
	cfg.PeerLatency = 500 * time.Millisecond
	return cfg
}

// dialPublisher connects an SRT caller through srtla_send, retrying while
// the downstream listener is not ready yet.
func dialPublisher(t *testing.T, port int, passphrase string, timeout time.Duration) srt.Conn {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for {
		conn, err := srt.Dial("srt", addr, publisherConfig(passphrase))
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial publisher via srtla_send: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// startPublisher streams at roughly mbps until stopped.
func startPublisher(t *testing.T, conn srt.Conn, epoch uint32, mbps float64) *publisher {
	t.Helper()
	p := &publisher{conn: conn, epoch: epoch, stop: make(chan struct{}), done: make(chan struct{})}
	const tick = 5 * time.Millisecond
	perTick := mbps * 1e6 / 8 / messageSize * tick.Seconds()
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		var budget float64
		msg := make([]byte, messageSize)
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
			}
			for budget += perTick; budget >= 1; budget-- {
				p.mu.Lock()
				seq := p.sent
				p.mu.Unlock()
				encodeMessage(msg, epoch, seq)
				if _, err := conn.Write(msg); err != nil {
					p.mu.Lock()
					p.err = err
					p.mu.Unlock()
					return
				}
				p.mu.Lock()
				p.sent++
				p.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() { p.close() })
	return p
}

func (p *publisher) sentCount() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sent
}

// pause stops writing but keeps the connection open.
func (p *publisher) pause() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	<-p.done
}

func (p *publisher) close() {
	p.pause()
	p.conn.Close()
}

func encodeMessage(msg []byte, epoch uint32, seq uint64) {
	for i := 0; i < len(msg); i += tsPacketSize {
		msg[i] = 0x47
		for j := i + 1; j < i+tsPacketSize; j++ {
			msg[j] = byte(j - i)
		}
	}
	binary.BigEndian.PutUint32(msg[4:], epoch)
	binary.BigEndian.PutUint64(msg[8:], seq)
}

// udpSink stands in for OBS: it receives go-irl's UDP output and checks
// every datagram is an intact publisher message.
type udpSink struct {
	conn *net.UDPConn

	mu      sync.Mutex
	seqs    map[uint32][]uint64 // epoch → sequence numbers in arrival order
	corrupt int
}

func startUDPSink(t *testing.T, port int) *udpSink {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	s := &udpSink{conn: conn, seqs: map[uint32][]uint64{}}
	want := make([]byte, messageSize)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			s.mu.Lock()
			if n != messageSize {
				s.corrupt++
				s.mu.Unlock()
				continue
			}
			epoch := binary.BigEndian.Uint32(buf[4:])
			seq := binary.BigEndian.Uint64(buf[8:])
			encodeMessage(want, epoch, seq)
			if !bytes.Equal(buf[:n], want) {
				s.corrupt++
			} else {
				s.seqs[epoch] = append(s.seqs[epoch], seq)
			}
			s.mu.Unlock()
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return s
}

func (s *udpSink) received(epoch uint32) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seqs[epoch])
}

// requireIntact asserts that epoch arrived exactly in order from its first
// received message through the last one the publisher sent.
func (s *udpSink) requireIntact(t *testing.T, epoch uint32, sent uint64) {
	t.Helper()
	eventually(t, 5*time.Second, "output to drain", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		seqs := s.seqs[epoch]
		return len(seqs) > 0 && seqs[len(seqs)-1] == sent-1
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.corrupt > 0 {
		t.Fatalf("%d corrupt datagrams in UDP output", s.corrupt)
	}
	seqs := s.seqs[epoch]
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("epoch %d: sequence jumps from %d to %d at datagram %d", epoch, seqs[i-1], seqs[i], i)
		}
	}
	// SRT may drop what it buffered before go-irl's reader attached, but
	// nothing after the stream started flowing.
	if seqs[0] > sent/10 {
		t.Fatalf("epoch %d: output started at %d of %d messages", epoch, seqs[0], sent)
	}
}

// statsMessage mirrors go-irl's WebSocket payload.
type statsMessage struct {
	Timestamp time.Time      `json:"timestamp"`
	Type      string         `json:"type"`
	Stats     srt.Statistics `json:"stats"`
}

// wsClient stands in for the Browser Source: it records every statistics
// message go-irl pushes over the WebSocket.
type wsClient struct {
	mu   sync.Mutex
	raw  []json.RawMessage
	msgs []statsMessage
	bad  []string
}

func connectWS(t *testing.T, port int) *wsClient {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: "/ws"}
	var conn *websocket.Conn
	eventually(t, 10*time.Second, "WebSocket to accept", func() bool {
		var err error
		conn, _, err = websocket.DefaultDialer.Dial(u.String(), nil)
		return err == nil
	})
	w := &wsClient{}
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m statsMessage
			w.mu.Lock()
			if err := json.Unmarshal(data, &m); err != nil {
				w.bad = append(w.bad, string(data))
			} else {
				w.raw = append(w.raw, json.RawMessage(bytes.Clone(data)))
				w.msgs = append(w.msgs, m)
			}
			w.mu.Unlock()
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return w
}

func (w *wsClient) messages() []statsMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]statsMessage(nil), w.msgs...)
}

func (w *wsClient) readers() []statsMessage {
	var out []statsMessage
	for _, m := range w.messages() {
		if m.Type == "reader" {
			out = append(out, m)
		}
	}
	return out
}

func (w *wsClient) mark() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.msgs)
}

// readersSince returns reader messages received after mark.
func (w *wsClient) readersSince(mark int) []statsMessage {
	var out []statsMessage
	for _, m := range w.messages()[mark:] {
		if m.Type == "reader" {
			out = append(out, m)
		}
	}
	return out
}

func (w *wsClient) waitReaders(t *testing.T, mark, n int, timeout time.Duration, what string, ok func(statsMessage) bool) []statsMessage {
	t.Helper()
	var got []statsMessage
	eventually(t, timeout, what, func() bool {
		got = got[:0]
		for _, m := range w.readersSince(mark) {
			if ok == nil || ok(m) {
				got = append(got, m)
			}
		}
		return len(got) >= n
	})
	return got
}

// requireWellFormed checks the invariants the Browser Source depends on.
func (w *wsClient) requireWellFormed(t *testing.T) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.bad) > 0 {
		t.Fatalf("undecodable WebSocket messages: %q", w.bad)
	}
	for _, m := range w.msgs {
		if m.Type != "reader" && m.Type != "writer" {
			t.Fatalf("unexpected message type %q", m.Type)
		}
		if m.Timestamp.IsZero() || time.Since(m.Timestamp) > 5*time.Minute {
			t.Fatalf("bad timestamp %v", m.Timestamp)
		}
	}
}

// dump saves the captured messages for the frontend contract test
// (frontend/src/e2eStats.test.ts) when E2E_STATS_DIR is set.
func (w *wsClient) dump(t *testing.T, expect map[string]any) {
	t.Helper()
	dir := os.Getenv("E2E_STATS_DIR")
	if dir == "" {
		return
	}
	w.mu.Lock()
	out := map[string]any{"test": t.Name(), "expect": expect, "messages": w.raw}
	data, err := json.MarshalIndent(out, "", " ")
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := strings.ReplaceAll(t.Name(), "/", "_") + ".json"
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// dialOnce makes a single SRT connection attempt through srtla_send.
func dialOnce(port int, passphrase string) (srt.Conn, error) {
	return srt.Dial("srt", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), publisherConfig(passphrase))
}
