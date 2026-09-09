package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// Tests using globals must remain serial. UDP responses are read with deadlines.
func udpTestSocket(t *testing.T) *net.UDPConn {
	t.Helper()
	s, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func setupSRTLA(t *testing.T) *net.UDPConn {
	t.Helper()
	oldGroups, oldSock, oldAddr := groups, srtlaSock, srtAddr
	srtlaSock = udpTestSocket(t)
	downstream := udpTestSocket(t)
	groups = nil
	srtAddr = downstream.LocalAddr().(*net.UDPAddr)
	t.Cleanup(func() {
		for _, g := range groups {
			g.close()
		}
		groups, srtlaSock, srtAddr = oldGroups, oldSock, oldAddr
	})
	return downstream
}
func readUDP(t *testing.T, s *net.UDPConn) []byte {
	t.Helper()
	if err := s.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 2048)
	n, _, err := s.ReadFromUDP(b)
	if err != nil {
		t.Fatal(err)
	}
	return b[:n]
}
func noUDP(t *testing.T, s *net.UDPConn) {
	t.Helper()
	s.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	_, _, err := s.ReadFromUDP(make([]byte, 2048))
	if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatalf("expected silence, got %v", err)
	}
}

// Literal wire values are intentionally independent of receiver constants.
// See docs/testing.md for the pinned Moblin sender reference.
func moblinPacket(kind uint16, size int) []byte {
	b := make([]byte, size)
	binary.BigEndian.PutUint16(b, kind)
	return b
}
func moblinRegister(t *testing.T, client *net.UDPConn) []byte {
	t.Helper()
	reg1 := moblinPacket(0x9200, 258)
	for i := 2; i < len(reg1); i++ {
		reg1[i] = byte(i)
	}
	handleSRTLAIncoming(reg1, client.LocalAddr().(*net.UDPAddr))
	reg2 := readUDP(t, client)
	if len(reg2) != 258 || !bytes.Equal(reg2[:2], []byte{0x92, 1}) || !bytes.Equal(reg2[2:130], reg1[2:130]) {
		t.Fatalf("Moblin would reject REG2: %x", reg2)
	}
	handleSRTLAIncoming(reg2, client.LocalAddr().(*net.UDPAddr))
	if got := readUDP(t, client); !bytes.Equal(got, []byte{0x92, 2}) {
		t.Fatalf("REG3: %x", got)
	}
	return reg2
}
func TestMoblinRegistrationAndErrors(t *testing.T) {
	setupSRTLA(t)
	a, b := udpTestSocket(t), udpTestSocket(t)
	reg2 := moblinRegister(t, a)
	for i := 0; i < 2; i++ {
		handleSRTLAIncoming(reg2, b.LocalAddr().(*net.UDPAddr))
		if got := readUDP(t, b); !bytes.Equal(got, []byte{0x92, 2}) {
			t.Fatalf("REG3: %x", got)
		}
	}
	if len(groups) != 1 || len(groups[0].conns) != 2 {
		t.Fatal("registration is not idempotent")
	}
	invalid := bytes.Clone(reg2)
	invalid[257] ^= 1
	handleSRTLAIncoming(invalid, b.LocalAddr().(*net.UDPAddr))
	if got := readUDP(t, b); !bytes.Equal(got, []byte{0x92, 0x11}) {
		t.Fatalf("REG_NGP: %x", got)
	}
	handleSRTLAIncoming(moblinPacket(0x9200, 258), a.LocalAddr().(*net.UDPAddr))
	if got := readUDP(t, a); !bytes.Equal(got, []byte{0x92, 0x10}) {
		t.Fatalf("REG_ERR: %x", got)
	}
}
func TestMoblinBondingWireCompatibility(t *testing.T) {
	downstream := setupSRTLA(t)
	a, b := udpTestSocket(t), udpTestSocket(t)
	reg2 := moblinRegister(t, a)
	handleSRTLAIncoming(reg2, b.LocalAddr().(*net.UDPAddr))
	readUDP(t, b)
	g := groups[0]
	// Duplicates, out-of-order packets and the 31-bit wrap must survive unchanged.
	seqs := []uint32{0x7ffffffe, 0x7fffffff, 0, 2, 1, 2, 3, 4, 5, 6}
	for batch := 0; batch < 2; batch++ {
		for _, sn := range seqs {
			pkt := make([]byte, 32)
			binary.BigEndian.PutUint32(pkt, sn)
			copy(pkt[16:], "mpeg-ts payload")
			handleSRTLAIncoming(pkt, a.LocalAddr().(*net.UDPAddr))
			if got := readUDP(t, downstream); !bytes.Equal(got, pkt) {
				t.Fatal("SRT packet changed")
			}
		}
		ack := readUDP(t, a)
		if len(ack) != 44 || !bytes.Equal(ack[:4], []byte{0x91, 0, 0, 0}) {
			t.Fatalf("ACK framing: %x", ack)
		}
		for i, sn := range seqs {
			if binary.BigEndian.Uint32(ack[4+4*i:]) != sn {
				t.Fatalf("ACK sequence %d", i)
			}
		}
		noUDP(t, b)
	}
	keepalive := []byte{0x90, 0, 0, 0, 0, 0, 0, 0, 0x12, 0x34}
	handleSRTLAIncoming(keepalive, b.LocalAddr().(*net.UDPAddr))
	if got := readUDP(t, b); !bytes.Equal(got, keepalive) {
		t.Fatal("RTT timestamp changed")
	}
	if !udpAddrEqual(g.lastAddr, a.LocalAddr().(*net.UDPAddr)) {
		t.Fatal("keepalive stole return path")
	}
	noUDP(t, downstream)
	for _, kind := range []uint16{0x8002, 0x8003} {
		pkt := moblinPacket(kind, 20)
		binary.BigEndian.PutUint32(pkt[16:], 7)
		g.mu.Lock()
		returnAddr := g.srtSock.LocalAddr().(*net.UDPAddr)
		g.mu.Unlock()
		if _, err := downstream.WriteToUDP(pkt, returnAddr); err != nil {
			t.Fatal(err)
		}
		for _, c := range []*net.UDPConn{a, b} {
			if got := readUDP(t, c); !bytes.Equal(got, pkt) {
				t.Fatal("ACK/NAK broadcast changed")
			}
		}
	}
	pkt := moblinPacket(0x8000, 64)
	g.mu.Lock()
	returnAddr := g.srtSock.LocalAddr().(*net.UDPAddr)
	g.mu.Unlock()
	if _, err := downstream.WriteToUDP(pkt, returnAddr); err != nil {
		t.Fatal(err)
	}
	if got := readUDP(t, a); !bytes.Equal(got, pkt) {
		t.Fatal("handshake changed")
	}
	noUDP(t, b)
	// Other SRT control packets are forwarded but do not count toward SRTLA ACKs.
	handleSRTLAIncoming(pkt, b.LocalAddr().(*net.UDPAddr))
	if got := readUDP(t, downstream); !bytes.Equal(got, pkt) {
		t.Fatal("upstream handshake changed")
	}
	if g.conns[1].recvIdx != 0 {
		t.Fatal("control packet acknowledged as data")
	}
}
func TestSRTLAMalformedAndUnregistered(t *testing.T) {
	downstream := setupSRTLA(t)
	c := udpTestSocket(t)
	for _, p := range [][]byte{nil, {0x92}, moblinPacket(0x9200, 257), moblinPacket(0x9201, 259), make([]byte, 16), {0x90, 0}} {
		handleSRTLAIncoming(p, c.LocalAddr().(*net.UDPAddr))
	}
	if len(groups) != 0 {
		t.Fatal("malformed packet created group")
	}
	noUDP(t, c)
	noUDP(t, downstream)
	moblinRegister(t, c)
	for n := 0; n < 16; n++ {
		handleSRTLAIncoming(make([]byte, n), c.LocalAddr().(*net.UDPAddr))
	}
	noUDP(t, c)
	noUDP(t, downstream)
}
func TestSRTLACleanup(t *testing.T) {
	setupSRTLA(t)
	a, b := udpTestSocket(t), udpTestSocket(t)
	reg2 := moblinRegister(t, a)
	handleSRTLAIncoming(reg2, b.LocalAddr().(*net.UDPAddr))
	readUDP(t, b)
	g := groups[0]
	g.createdAt = time.Now().Add(-time.Minute)
	g.conns[0].lastRcvd.Store(time.Now().Add(-20 * time.Second).UnixNano())
	g.conns[1].lastRcvd.Store(time.Now().Add(-2 * time.Second).UnixNano())
	cleanup()
	if len(groups) != 1 || len(g.conns) != 1 {
		t.Fatal("active group lost or expired connection retained")
	}
	if got := readUDP(t, b); !bytes.Equal(got, []byte{0x90, 0}) {
		t.Fatalf("keepalive: %x", got)
	}
	g.conns[0].lastRcvd.Store(time.Now().Add(-20 * time.Second).UnixNano())
	cleanup()
	if len(groups) != 0 {
		t.Fatal("expired group retained")
	}
}
func TestSRTLACapacity(t *testing.T) {
	setupSRTLA(t)
	c := udpTestSocket(t)
	reg2 := moblinRegister(t, c)
	g := groups[0]
	for len(g.conns) < MaxConnsPerGroup {
		g.conns = append(g.conns, &Conn{addr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: len(g.conns)}})
	}
	other := udpTestSocket(t)
	handleSRTLAIncoming(reg2, other.LocalAddr().(*net.UDPAddr))
	if got := readUDP(t, other); !bytes.Equal(got, []byte{0x92, 0x10}) {
		t.Fatalf("capacity: %x", got)
	}
	for len(groups) < MaxGroups {
		groups = append(groups, &Group{})
	}
	handleSRTLAIncoming(moblinPacket(0x9200, 258), other.LocalAddr().(*net.UDPAddr))
	if got := readUDP(t, other); !bytes.Equal(got, []byte{0x92, 0x10}) {
		t.Fatalf("group capacity: %x", got)
	}
}
func FuzzSRTPacketParsing(f *testing.F) {
	for _, p := range [][]byte{nil, {0x90, 0}, moblinPacket(0x9200, 258), {0x7f, 0xff, 0xff, 0xff}} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p []byte) {
		sn := getSRTSN(p)
		if len(p) < 4 || p[0]&0x80 != 0 {
			if sn != -1 {
				t.Fatal("invalid data sequence")
			}
		} else if uint32(sn) != binary.BigEndian.Uint32(p[:4]) {
			t.Fatal("sequence changed")
		}
		if isSRTLAReg1(p) != (len(p) == 258 && p[0] == 0x92 && p[1] == 0) {
			t.Fatal("REG1 classification")
		}
		if isSRTLAReg2(p) != (len(p) == 258 && p[0] == 0x92 && p[1] == 1) {
			t.Fatal("REG2 classification")
		}
	})
}

func TestSRTLAConcurrentAddressLookup(t *testing.T) {
	setupSRTLA(t)
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	g := &Group{}
	groups = []*Group{g}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			g.mu.Lock()
			g.conns = []*Conn{{addr: addr}}
			g.lastAddr = addr
			g.mu.Unlock()
			g.mu.Lock()
			g.conns = nil
			g.lastAddr = nil
			g.mu.Unlock()
		}
	}()
	for i := 0; i < 1000; i++ {
		findByAddr(addr)
	}
	<-done
}
