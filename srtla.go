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

	SRTMinLen = 16 // minimum SRT packet length (srt_header_t)

	SRTTypeHandshake = 0x8000
	SRTTypeACK       = 0x8002
	SRTTypeNAK       = 0x8003
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

	CleanupPeriod   = 3 * time.Second
	GroupTimeout    = 4 * time.Second
	ConnTimeout     = 4 * time.Second
	KeepalivePeriod = 1 * time.Second

	SendBufSize = 100 * 1024 * 1024 // 100 MB
	RecvBufSize = 100 * 1024 * 1024 // 100 MB

	// srt_handshake_t size: srt_header_t(16) + version(4) + enc_field(2) +
	// ext_field(2) + initial_seq(4) + mtu(4) + mfw(4) + handshake_type(4) +
	// source_id(4) + syn_cookie(4) + peer_ip(16) = 64
	SRTHandshakeSize = 64
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
	recvIdx  int                     // next slot in recvLog
	recvLog  [RecvACKInterval]uint32 // SRT sequence numbers for SRTLA ACK
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
func isSRTNak(pkt []byte) bool         { return getSRTType(pkt) == SRTTypeNAK }
func isSRTLAKeepalive(pkt []byte) bool { return getSRTType(pkt) == SRTLATypeKeepalive }

// getSRTSN returns the SRT sequence number from a data packet (bit 31 == 0).
// Returns -1 for control packets or packets too short.
func getSRTSN(pkt []byte) int32 {
	if len(pkt) < 4 {
		return -1
	}
	sn := binary.BigEndian.Uint32(pkt[:4])
	if sn&(1<<31) == 0 {
		return int32(sn)
	}
	return -1
}

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
	copy(clientID, pkt[2:2+SRTLAIDLen/2])
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

	g.mu.Lock()
	// Check for existing connection entry
	var existingConn *Conn
	for _, c := range g.conns {
		if udpAddrEqual(c.addr, addr) {
			existingConn = c
			break
		}
	}

	if existingConn == nil && len(g.conns) >= MaxConnsPerGroup {
		g.mu.Unlock()
		sendRegErr(addr)
		log.Printf("[%s] [group %p] Conn registration failed: Too many conns", addr, g)
		return
	}
	g.mu.Unlock()

	// Send REG3 response – only add connection if send succeeds (matches C++)
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], SRTLATypeReg3)
	if _, err := srtlaSock.WriteToUDP(hdr[:], addr); err != nil {
		log.Printf("[%s] [group %p] Conn registration failed: Socket send error: %v", addr, g, err)
		return
	}

	g.mu.Lock()
	if existingConn == nil {
		g.conns = append(g.conns, &Conn{addr: addr, lastRcvd: time.Now()})
	}
	g.lastAddr = addr
	g.mu.Unlock()

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
			if err != nil || n < SRTMinLen {
				log.Printf("[group %p] Failed to read the SRT sock (n=%d, err=%v), terminating the group", g, n, err)
				removeGroup(g)
				return
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			handleSRTData(g, pkt)
		}
	}()
}

func handleSRTData(g *Group, pkt []byte) {
	if len(pkt) < SRTMinLen {
		return
	}

	// Broadcast ACKs and NAKs to all connections so they reach the sender
	// even if some connections are dead. Other packets go to last_address.
	if isSRTAck(pkt) || isSRTNak(pkt) {
		g.mu.Lock()
		conns := make([]*Conn, len(g.conns))
		copy(conns, g.conns)
		g.mu.Unlock()
		for _, c := range conns {
			if _, err := srtlaSock.WriteToUDP(pkt, c.addr); err != nil {
				log.Printf("[%s] [group %p] Failed to fwd SRT ACK/NAK: %v", c.addr, g, err)
			}
		}
	} else {
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

	if isSRTLAKeepalive(pkt) {
		// Echo back the keepalive.  Do NOT update lastAddr for keepalives
		srtlaSock.WriteToUDP(pkt, addr)
		return
	}

	// Non-keepalive packet – must be at least SRT minimum length
	if len(pkt) < SRTMinLen {
		return
	}

	// Update lastAddr only for real SRT data/control packets
	g.mu.Lock()
	g.lastAddr = addr
	g.mu.Unlock()

	// Register packet sequence number and send SRTLA ACK when buffer is full
	sn := getSRTSN(pkt)
	if sn >= 0 {
		registerPacket(g, c, sn)
	}

	// Forward to SRT socket, creating it if needed
	if !ensureGroupSocket(g) {
		return
	}

	g.mu.Lock()
	srtConn := g.srtSock
	g.mu.Unlock()
	if srtConn == nil {
		return
	}

	_, err := srtConn.Write(pkt)
	if err != nil {
		log.Printf("[group %p] Failed to forward SRTLA packet, terminating the group: %v", g, err)
		removeGroup(g)
	}
}

// ensureGroupSocket creates the SRT socket for a group if it doesn't exist.
// Returns true if the socket is ready.
func ensureGroupSocket(g *Group) bool {
	g.mu.Lock()
	if g.srtSock != nil {
		g.mu.Unlock()
		return true
	}
	g.mu.Unlock()

	conn, err := net.DialUDP("udp", nil, srtAddr)
	if err != nil {
		log.Printf("[group %p] Failed to create an SRT socket: %v", g, err)
		removeGroup(g)
		return false
	}
	if err := conn.SetReadBuffer(RecvBufSize); err != nil {
		log.Printf("[group %p] Failed to set receive buffer: %v", g, err)
		conn.Close()
		removeGroup(g)
		return false
	}
	if err := conn.SetWriteBuffer(SendBufSize); err != nil {
		log.Printf("[group %p] Failed to set send buffer: %v", g, err)
		conn.Close()
		removeGroup(g)
		return false
	}

	g.mu.Lock()
	// Double-check – another goroutine might have created it
	if g.srtSock != nil {
		g.mu.Unlock()
		conn.Close()
		return true
	}
	g.srtSock = conn
	g.mu.Unlock()

	log.Printf("[group %p] Created SRT socket (local %s)", g, conn.LocalAddr())
	startSRTReader(g)
	return true
}

// registerPacket logs a received SRT data packet's sequence number and,
// once RecvACKInterval packets have been logged, sends an SRTLA ACK back
// to the sender.
func registerPacket(g *Group, c *Conn, sn int32) {
	idx := c.recvIdx + 1
	if idx <= 0 || idx > RecvACKInterval {
		idx = 1
	}
	c.recvIdx = idx
	c.recvLog[idx-1] = uint32(sn)

	if c.recvIdx == RecvACKInterval {
		// Build srtla_ack_pkt: 4 bytes type + RecvACKInterval * 4 bytes
		var ack [4 + RecvACKInterval*4]byte
		binary.BigEndian.PutUint32(ack[0:4], uint32(SRTLATypeACK)<<16)
		for i := 0; i < RecvACKInterval; i++ {
			binary.BigEndian.PutUint32(ack[4+i*4:], c.recvLog[i])
		}
		if _, err := srtlaSock.WriteToUDP(ack[:], c.addr); err != nil {
			log.Printf("[%s] [group %p] Failed to send the SRTLA ACK: %v", c.addr, g, err)
		}
		c.recvIdx = 0
	}
}

func sendKeepalive(c *Conn) {
	var pkt [2]byte
	binary.BigEndian.PutUint16(pkt[:], SRTLATypeKeepalive)
	srtlaSock.WriteToUDP(pkt[:], c.addr)
}

func cleanup() {
	now := time.Now()

	groupsMu.Lock()
	defer groupsMu.Unlock()

	var newGroups []*Group
	for _, g := range groups {
		g.mu.Lock()
		var newConns []*Conn
		for _, c := range g.conns {
			if now.Sub(c.lastRcvd) >= ConnTimeout {
				log.Printf("[%s] [group %p] Connection removed (timed out)", c.addr, g)
				continue
			}
			// Send keepalive to connections that haven't been heard from recently
			if now.Sub(c.lastRcvd) >= KeepalivePeriod {
				sendKeepalive(c)
			}
			newConns = append(newConns, c)
		}
		if len(newConns) != len(g.conns) {
			g.conns = newConns
		}

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

	// Build srt_handshake_t matching the C++ struct layout:
	//   srt_header_t (16 bytes): type(2) + subtype(2) + info(4) + timestamp(4) + dest_id(4)
	//   version(4) + enc_field(2) + ext_field(2) + initial_seq(4) + mtu(4) + mfw(4) +
	//   handshake_type(4) + source_id(4) + syn_cookie(4) + peer_ip(16) = 64 total
	hsPkt := make([]byte, SRTHandshakeSize)
	binary.BigEndian.PutUint16(hsPkt[0:], SRTTypeHandshake) // header.type
	// header.subtype, info, timestamp, dest_id all zero
	binary.BigEndian.PutUint32(hsPkt[16:], 4) // version
	// enc_field(2) at offset 20 = 0
	binary.BigEndian.PutUint16(hsPkt[22:], 2) // ext_field
	// initial_seq(4) at offset 24 = 0, mtu(4) at 28 = 0, mfw(4) at 32 = 0
	binary.BigEndian.PutUint32(hsPkt[36:], 1) // handshake_type = induction

	for _, ip := range addrs {
		raddr := &net.UDPAddr{IP: ip, Port: int(port)}
		log.Printf("Trying to connect to SRT at %s ...", raddr)
		conn, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			continue
		}
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = conn.Write(hsPkt)
		if err == nil {
			buf := make([]byte, MTU)
			n, err := conn.Read(buf)
			if err == nil && n == SRTHandshakeSize {
				conn.Close()
				return raddr, nil
			}
			log.Printf("Failed to receive handshake response (n=%d)", n)
		}
		conn.Close()
	}
	// Fallback to first IP even if handshake failed
	if len(addrs) == 0 {
		return nil, fmt.Errorf("No IP addresses found for host %s", host)
	}
	log.Printf("Warning: Failed to confirm SRT server is reachable. Proceeding with first address.")
	return &net.UDPAddr{IP: addrs[0], Port: int(port)}, nil
}

func runSrtla(srtlaPort uint, srtHost string, srtPort uint, verbose bool) {
	if verbose {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
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

// removeGroup deletes the group from global slice and closes its SRT socket.
func removeGroup(g *Group) {
	g.close()

	groupsMu.Lock()
	defer groupsMu.Unlock()
	for i, gg := range groups {
		if gg == g {
			groups = append(groups[:i], groups[i+1:]...)
			return
		}
	}
}

func (g *Group) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.srtSock != nil {
		g.srtSock.Close()
		g.srtSock = nil
	}
}
