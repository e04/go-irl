// fakesrtla is a minimal SRTLA sender for trying go-irl locally.
//
//	SRT caller (srtpub) → -listen → N bonded UDP links → go-irl -server
//
// Packets are spread round-robin over the registered links. With -chaos it
// alternates between a stable phase and a phase with random per-link loss,
// single-link outages and total outages. It re-registers automatically when
// go-irl restarts.
package main

import (
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	mrand "math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	typeKeepalive = 0x9000
	typeACK       = 0x9100
	typeReg1      = 0x9200
	typeReg2      = 0x9201
	typeReg3      = 0x9202
	typeRegErr    = 0x9210
	typeRegNGP    = 0x9211
	idLen         = 256
)

type link struct {
	conn *net.UDPConn
	up   atomic.Bool   // registered with go-irl
	down atomic.Bool   // simulated outage: nothing sent or received
	loss atomic.Uint64 // simulated loss rate in 1/1000
	sent atomic.Uint64
}

type sender struct {
	links   []*link
	allDown atomic.Bool

	mu      sync.Mutex
	groupID []byte // nil until REG2 is received
	peer    *net.UDPAddr
}

func main() {
	listen := flag.String("listen", "127.0.0.1:6000", "local address the SRT caller sends to")
	server := flag.String("server", "127.0.0.1:5000", "go-irl SRTLA address")
	n := flag.Int("links", 3, "number of bonded links")
	loss := flag.Float64("loss", 0, "fixed packet loss rate (0-1) on link 0")
	outage := flag.Duration("outage", 8*time.Second, "how long SIGUSR1 takes every link down")
	chaosMode := flag.Bool("chaos", false, "alternate stable phases with random loss and outages")
	flag.Parse()

	srv, err := net.ResolveUDPAddr("udp", *server)
	if err != nil {
		log.Fatal(err)
	}
	laddr, err := net.ResolveUDPAddr("udp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	local, err := net.ListenUDP("udp", laddr)
	if err != nil {
		log.Fatal(err)
	}

	s := &sender{links: make([]*link, *n)}
	for i := range s.links {
		c, err := net.DialUDP("udp", nil, srv)
		if err != nil {
			log.Fatal(err)
		}
		s.links[i] = &link{conn: c}
	}
	for i, l := range s.links {
		go s.readLink(i, l, local)
	}
	go s.maintain(srv)
	go s.report()

	if *chaosMode {
		go s.chaos()
	} else if *loss > 0 {
		s.links[0].loss.Store(uint64(*loss * 1000))
	}

	// `kill -USR1 <pid>` takes every link down for -outage.
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for range usr1 {
			log.Printf("SIGUSR1: ALL LINKS DOWN for %s", *outage)
			s.allDown.Store(true)
			time.Sleep(*outage)
			s.allDown.Store(false)
			log.Printf("SIGUSR1: all links restored")
		}
	}()

	log.Printf("send SRT (caller) to srt://%s", *listen)
	buf := make([]byte, 1500)
	var rr int
	for {
		nr, from, err := local.ReadFromUDP(buf)
		if err != nil {
			log.Fatal(err)
		}
		s.mu.Lock()
		s.peer = from
		s.mu.Unlock()
		for range s.links {
			rr = (rr + 1) % len(s.links)
			l := s.links[rr]
			if !l.up.Load() || l.down.Load() {
				continue
			}
			if s.allDown.Load() || mrand.Intn(1000) < int(l.loss.Load()) {
				break
			}
			l.conn.Write(buf[:nr])
			l.sent.Add(1)
			break
		}
	}
}

// readLink handles SRTLA control replies and relays SRT packets back to the caller.
func (s *sender) readLink(i int, l *link, local *net.UDPConn) {
	buf := make([]byte, 1500)
	for {
		nr, err := l.conn.Read(buf)
		if err != nil {
			// e.g. ICMP port unreachable while go-irl is not running
			time.Sleep(100 * time.Millisecond)
			continue
		}
		pkt := buf[:nr]
		if l.down.Load() || s.allDown.Load() {
			continue
		}
		if nr >= 2 {
			switch binary.BigEndian.Uint16(pkt) {
			case typeReg2:
				s.mu.Lock()
				if s.groupID == nil {
					s.groupID = append([]byte(nil), pkt[2:]...)
					log.Printf("group registered")
				}
				s.mu.Unlock()
				continue
			case typeReg3:
				if !l.up.Swap(true) {
					log.Printf("link %d (%s) registered", i, l.conn.LocalAddr())
				}
				continue
			case typeRegNGP:
				// go-irl no longer knows the group (restarted or timed out).
				s.mu.Lock()
				if s.groupID != nil {
					log.Printf("link %d: group lost, registering again", i)
					s.groupID = nil
					for _, l := range s.links {
						l.up.Store(false)
					}
				}
				s.mu.Unlock()
				continue
			case typeKeepalive, typeACK, typeRegErr:
				continue
			}
		}
		s.mu.Lock()
		p := s.peer
		s.mu.Unlock()
		if p != nil {
			local.WriteToUDP(pkt, p)
		}
	}
}

// maintain registers the group and links and sends keepalives every second.
func (s *sender) maintain(srv *net.UDPAddr) {
	reg1 := make([]byte, 2+idLen)
	binary.BigEndian.PutUint16(reg1, typeReg1)
	ka := make([]byte, 2)
	binary.BigEndian.PutUint16(ka, typeKeepalive)
	for ; ; time.Sleep(time.Second) {
		if s.allDown.Load() {
			continue
		}
		s.mu.Lock()
		id := s.groupID
		s.mu.Unlock()
		if id == nil {
			rand.Read(reg1[2 : 2+idLen/2])
			if l := s.firstAvailable(); l != nil {
				l.conn.Write(reg1)
			}
			log.Printf("registering with go-irl at %s ...", srv)
			continue
		}
		reg2 := make([]byte, 2+idLen)
		binary.BigEndian.PutUint16(reg2, typeReg2)
		copy(reg2[2:], id)
		for _, l := range s.links {
			switch {
			case l.down.Load():
			case !l.up.Load():
				l.conn.Write(reg2)
			default:
				l.conn.Write(ka)
			}
		}
	}
}

func (s *sender) firstAvailable() *link {
	for _, l := range s.links {
		if !l.down.Load() {
			return l
		}
	}
	return nil
}

func (s *sender) report() {
	const period = 5
	for range time.Tick(period * time.Second) {
		var b strings.Builder
		for i, l := range s.links {
			st := fmt.Sprintf("%dpps", l.sent.Swap(0)/period)
			switch {
			case l.down.Load() || s.allDown.Load():
				st = "DOWN"
			case !l.up.Load():
				st = "unregistered"
			case l.loss.Load() > 0:
				st += fmt.Sprintf("(loss %.1f%%)", float64(l.loss.Load())/10)
			}
			fmt.Fprintf(&b, " %d=%s", i, st)
		}
		log.Printf("links:%s", b.String())
	}
}

// chaos alternates between a stable phase (everything healthy) and a phase
// of random loss and outages.
func (s *sender) chaos() {
	var gen atomic.Int64 // bumped on each stable phase so pending recoveries are dropped
	for {
		gen.Add(1)
		s.allDown.Store(false)
		for _, l := range s.links {
			l.loss.Store(0)
			if l.down.Swap(false) {
				l.up.Store(false) // re-register in case go-irl timed it out
			}
		}
		calm := time.Duration(20+mrand.Intn(21)) * time.Second
		log.Printf("===== STABLE for %s =====", calm)
		time.Sleep(calm)

		end := time.Now().Add(time.Duration(15+mrand.Intn(16)) * time.Second)
		log.Printf("===== CHAOS until %s =====", end.Format("15:04:05"))
		for time.Now().Before(end) {
			time.Sleep(time.Duration(3+mrand.Intn(6)) * time.Second)
			s.chaosEvent(&gen)
		}
	}
}

func (s *sender) chaosEvent(gen *atomic.Int64) {
	i := mrand.Intn(len(s.links))
	l := s.links[i]
	switch r := mrand.Intn(10); {
	case r < 4:
		v := uint64(mrand.Intn(300))
		l.loss.Store(v)
		log.Printf("CHAOS link %d loss -> %.1f%%", i, float64(v)/10)
	case r < 6:
		l.loss.Store(0)
		log.Printf("CHAOS link %d loss cleared", i)
	case r < 9:
		if l.down.Load() {
			return
		}
		d := time.Duration(5+mrand.Intn(21)) * time.Second
		l.down.Store(true)
		log.Printf("CHAOS link %d DOWN for %s", i, d)
		g := gen.Load()
		go func() {
			time.Sleep(d)
			if gen.Load() != g {
				return
			}
			l.down.Store(false)
			l.up.Store(false) // re-register in case go-irl timed it out
			log.Printf("CHAOS link %d back UP", i)
		}()
	default:
		d := time.Duration(2+mrand.Intn(7)) * time.Second
		s.allDown.Store(true)
		log.Printf("CHAOS ALL LINKS DOWN for %s", d)
		time.Sleep(d)
		s.allDown.Store(false)
		log.Printf("CHAOS all links restored")
	}
}
