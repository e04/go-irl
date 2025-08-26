package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net"
	"sync"
	"time"
)

const (
	MTU = 1500

	SRTTypeHandshake = 0x8000
	SRTTypeACK       = 0x8002
	SRTTypeShutdown  = 0x8005

	SRTLATypeKeepalive = 0x9000
	SRTLATypeACK       = 0x9100
	SRTLATypeReg1      = 0x9200
	SRTLATypeReg2      = 0x9201
	SRTLATypeReg3      = 0x9202
	SRTLATypeRegErr    = 0x9210
	SRTLATypeRegNGP    = 0x9211

	SRTLAIDLen   = 256
	SRTLAReg1Len = 2 + SRTLAIDLen
	SRTLAReg2Len = 2 + SRTLAIDLen
	SRTLAReg3Len = 2

	RecvACKInterval = 10 // number of pkts before sending SRT-LA ACK

	MaxConnsPerGroup = 16
	MaxGroups        = 200

	CleanupPeriod = 3 * time.Second
	GroupTimeout  = 10 * time.Second
	ConnTimeout   = 10 * time.Second

	SendBufSize = 32 * 1024 * 1024 // 32 MB
	RecvBufSize = 32 * 1024 * 1024
)

func constantTimeCompare(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		// crypto/rand should never fail on *nix, fall back to math/rand if it
		// ever does.
		log.Printf("Warning: crypto/rand failed (%v); falling back to pseudo-rand", err)
		for i := range b {
			b[i] = byte(mathrand.Intn(256))
		}
	}
	return b
}

func udpAddrEqual(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return false
	}
	return a.IP.Equal(b.IP) && a.Port == b.Port
}

type Conn struct {
	addr     *net.UDPAddr
	lastRcvd time.Time
}

type Group struct {
	id        [SRTLAIDLen]byte
	conns     []*Conn
	createdAt time.Time
	srtSock   *net.UDPConn // connection to downstream SRT server
	lastAddr  *net.UDPAddr // most recently active client addr
	mu        sync.Mutex   // protects conns + lastAddr + srtSock
}

var (
	groupsMu sync.RWMutex
	groups   []*Group

	srtlaSock *net.UDPConn
	srtAddr   *net.UDPAddr // resolved downstream SRT server address
)

func be16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }

func getSRTType(pkt []byte) uint16 {
	if len(pkt) < 2 {
		return 0
	}
	return be16(pkt[:2])
}

func isSRTAck(pkt []byte) bool         { return getSRTType(pkt) == SRTTypeACK }
func isSRTLAKeepalive(pkt []byte) bool { return getSRTType(pkt) == SRTLATypeKeepalive }

func isSRTLAReg1(pkt []byte) bool {
	return len(pkt) == SRTLAReg1Len && getSRTType(pkt) == SRTLATypeReg1
}
func isSRTLAReg2(pkt []byte) bool {
	return len(pkt) == SRTLAReg2Len && getSRTType(pkt) == SRTLATypeReg2
}

func findGroupByID(id []byte) *Group {
	groupsMu.RLock()
	defer groupsMu.RUnlock()
	for _, g := range groups {
		if constantTimeCompare(g.id[:], id) {
			return g
		}
	}
	return nil
}

func findByAddr(addr *net.UDPAddr) (g *Group, c *Conn) {
	groupsMu.RLock()
	defer groupsMu.RUnlock()
	for _, gr := range groups {
		for _, conn := range gr.conns {
			if udpAddrEqual(conn.addr, addr) {
				return gr, conn
			}
		}
		if udpAddrEqual(gr.lastAddr, addr) {
			return gr, nil
		}
	}
	return nil, nil
}

func newGroup(clientID []byte) *Group {
	var g Group
	g.createdAt = time.Now()

	copy(g.id[:SRTLAIDLen/2], clientID)
	copy(g.id[SRTLAIDLen/2:], randomBytes(SRTLAIDLen/2))
	return &g
}

func sendRegErr(addr *net.UDPAddr) {
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], SRTLATypeRegErr)
	_, _ = srtlaSock.WriteToUDP(header[:], addr)
}

func registerGroup(addr *net.UDPAddr, pkt []byte) {
	if len(groups) >= MaxGroups {
		log.Printf("[%s] Registration failed: Max groups reached", addr)
		sendRegErr(addr)
		return
	}

	// Prevent duplicate registration from same remote addr
	if g, _ := findByAddr(addr); g != nil {
		log.Printf("[%s] Registration failed: Addr already in group", addr)
		sendRegErr(addr)
		return
	}

	clientID := make([]byte, SRTLAIDLen/2)
	copy(clientID, pkt[2:])
	g := newGroup(clientID)

	// store last addr so that no other group can register from it
	g.lastAddr = addr

	// build REG2
	out := make([]byte, SRTLAReg2Len)
	binary.BigEndian.PutUint16(out[:2], SRTLATypeReg2)
	copy(out[2:], g.id[:])

	if _, err := srtlaSock.WriteToUDP(out, addr); err != nil {
		log.Printf("[%s] Registration failed: %v", addr, err)
		return
	}

	groupsMu.Lock()
	groups = append(groups, g)
	groupsMu.Unlock()

	log.Printf("[%s] [group %p] Registered", addr, g)
}

func registerConn(addr *net.UDPAddr, pkt []byte) {
	id := pkt[2:]
	g := findGroupByID(id)
	if g == nil {
		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], SRTLATypeRegNGP)
		srtlaSock.WriteToUDP(hdr[:], addr)
		log.Printf("[%s] Conn registration failed: no group", addr)
		return
	}

	// Reject if this addr is already tied to another group
	if tmp, _ := findByAddr(addr); tmp != nil && tmp != g {
		sendRegErr(addr)
		log.Printf("[%s] [group %p] Conn registration failed: Addr in other group", addr, g)
		return
	}

	var already bool

	g.mu.Lock()
	// Check for existing connection entry
	for _, c := range g.conns {
		if udpAddrEqual(c.addr, addr) {
			already = true
			break
		}
	}

	// Add new connection if necessary
	if !already {
		if len(g.conns) >= MaxConnsPerGroup {
			g.mu.Unlock()
			sendRegErr(addr)
			log.Printf("[%s] [group %p] Conn registration failed: Too many conns", addr, g)
			return
		}
		g.conns = append(g.conns, &Conn{addr: addr, lastRcvd: time.Now()})
	}

	// Update most-recent peer
	g.lastAddr = addr
	g.mu.Unlock()

	// Send REG3 response
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], SRTLATypeReg3)
	_, _ = srtlaSock.WriteToUDP(hdr[:], addr)

	log.Printf("[%s] [group %p] Conn Registered", addr, g)
}

func startSRTReader(g *Group) {
	go func() {
		buf := make([]byte, MTU)
		for {
			g.mu.Lock()
			conn := g.srtSock
			g.mu.Unlock()
			if conn == nil {
				return
			}
			n, err := conn.Read(buf)
			if err != nil {
				log.Printf("[group %p] SRT socket read error: %v", g, err)
				g.close()
				removeGroup(g)
				return
			}
			handleSRTData(g, buf[:n])
		}
	}()
}

func handleSRTData(g *Group, pkt []byte) {
	if len(pkt) < 4 {
		return
	}
	if isSRTAck(pkt) {
		// broadcast ACK to all conns
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, c := range g.conns {
			if _, err := srtlaSock.WriteToUDP(pkt, c.addr); err != nil {
				log.Printf("[%s] [group %p] Failed to fwd SRT ACK: %v", c.addr, g, err)
			}
		}
	} else {
		// send via last active conn
		g.mu.Lock()
		dst := g.lastAddr
		g.mu.Unlock()
		if dst != nil {
			if _, err := srtlaSock.WriteToUDP(pkt, dst); err != nil {
				log.Printf("[%s] [group %p] Failed to fwd SRT pkt: %v", dst, g, err)
			}
		}
	}
}

func handleSRTLAIncoming(pkt []byte, addr *net.UDPAddr) {
	now := time.Now()

	if isSRTLAReg1(pkt) {
		registerGroup(addr, pkt)
		return
	}
	if isSRTLAReg2(pkt) {
		registerConn(addr, pkt)
		return
	}

	g, c := findByAddr(addr)
	if g == nil || c == nil {
		return // not part of any group
	}

	c.lastRcvd = now
	g.lastAddr = addr

	if isSRTLAKeepalive(pkt) {
		// echo back
		srtlaSock.WriteToUDP(pkt, addr)
		return
	}

	// Forward to SRT socket, creating it if needed
	g.mu.Lock()
	if g.srtSock == nil {
		conn, err := net.DialUDP("udp", nil, srtAddr)
		if err != nil {
			g.mu.Unlock()
			log.Printf("[group %p] Failed to dial SRT server: %v", g, err)
			removeGroup(g)
			return
		}
		// increase buffers
		_ = conn.SetReadBuffer(RecvBufSize)
		_ = conn.SetWriteBuffer(SendBufSize)
		g.srtSock = conn
		g.mu.Unlock()
		startSRTReader(g)
		log.Printf("[group %p] Created SRT socket (local %s)", g, conn.LocalAddr())
	} else {
		g.mu.Unlock()
	}

	g.mu.Lock()
	srtConn := g.srtSock
	g.mu.Unlock()
	if srtConn == nil {
		return
	}

	_, err := srtConn.Write(pkt)
	if err != nil {
		log.Printf("[group %p] Failed to fwd SRTLA pkt: %v", g, err)
		g.close()
		removeGroup(g)
	}
}

func cleanup() {
	now := time.Now()

	groupsMu.Lock()
	defer groupsMu.Unlock()

	var newGroups []*Group
	for _, g := range groups {
		g.mu.Lock()
		// remove stale conns
		var newConns []*Conn
		for _, c := range g.conns {
			if now.Sub(c.lastRcvd) < ConnTimeout {
				newConns = append(newConns, c)
			} else {
				log.Printf("[%s] [group %p] Connection timed out", c.addr, g)
			}
		}
		if len(newConns) != len(g.conns) {
			g.conns = newConns
		}

		// decide if group should stay
		keep := true
		if len(g.conns) == 0 && now.Sub(g.createdAt) > GroupTimeout {
			keep = false
		}
		g.mu.Unlock()

		if keep {
			newGroups = append(newGroups, g)
		} else {
			log.Printf("[group %p] Removed (No connections)", g)
			g.close()
		}
	}
	groups = newGroups
}

func resolveSRTAddr(host string, port uint16) (*net.UDPAddr, error) {
	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil, err
	}

	hsPkt := make([]byte, 48) // sizeof(srt_handshake_t) in original code
	binary.BigEndian.PutUint16(hsPkt[0:], SRTTypeHandshake)
	binary.BigEndian.PutUint32(hsPkt[4:], 4)  // version
	binary.BigEndian.PutUint16(hsPkt[8:], 2)  // ext field
	binary.BigEndian.PutUint32(hsPkt[12:], 1) // handshake type = induction

	for _, ip := range addrs {
		raddr := &net.UDPAddr{IP: ip, Port: int(port)}
		conn, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			continue
		}
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = conn.Write(hsPkt)
		if err == nil {
			buf := make([]byte, MTU)
			n, err := conn.Read(buf)
			if err == nil && n == len(hsPkt) {
				conn.Close()
				return raddr, nil
			}
		}
		conn.Close()
	}
	// Fallback to first IP even if handshake failed
	if len(addrs) == 0 {
		return nil, fmt.Errorf("No IPs for host %s", host)
	}
	return &net.UDPAddr{IP: addrs[0], Port: int(port)}, nil
}

func runSrtla(srtlaPort uint, srtHost string, srtPort uint, verbose bool) {
	if verbose {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	} else {
		log.SetFlags(0)
	}

	var err error
	srtAddr, err = resolveSRTAddr(srtHost, uint16(srtPort))
	if err != nil {
		log.Fatalf("Could not resolve downstream SRT server: %v", err)
	}
	log.Printf("Downstream SRT server %s", srtAddr)

	// Listen UDP (dual-stack) for SRT-LA
	laddr := &net.UDPAddr{IP: net.IPv6unspecified, Port: int(srtlaPort)}
	srtlaSock, err = net.ListenUDP("udp", laddr)
	if err != nil {
		log.Fatalf("Failed to listen on UDP port %d: %v", srtlaPort, err)
	}
	_ = srtlaSock.SetReadBuffer(RecvBufSize)
	_ = srtlaSock.SetWriteBuffer(SendBufSize)

	log.Printf("Listening on %s", srtlaSock.LocalAddr())

	// Reader goroutine for SRT-LA socket
	go func() {
		buf := make([]byte, MTU)
		for {
			n, addr, err := srtlaSock.ReadFromUDP(buf)
			if err != nil {
				log.Printf("read error: %v", err)
				continue
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			handleSRTLAIncoming(pkt, addr)
		}
	}()

	// Periodic cleanup ticker
	ticker := time.NewTicker(CleanupPeriod)
	for range ticker.C {
		cleanup()
	}
}

// removeGroup deletes the group from global slice and frees its resources.
func removeGroup(g *Group) {
	groupsMu.Lock()
	defer groupsMu.Unlock()
	for i, gg := range groups {
		if gg == g {
			groups = append(groups[:i], groups[i+1:]...)
			break
		}
	}
	g.close()
}

func (g *Group) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.srtSock != nil {
		g.srtSock.Close()
		g.srtSock = nil
	}
}
