package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

var (
	mode    = flag.String("mode", "", "Operation mode: server | client | standalone (default: standalone)")
	srtPort = flag.Int("srt-port", 5001, "SRT port (standalone/server)")
	srtHost = flag.String("srt-host", "127.0.0.1", "SRT output host address (server mode)")

	srtlaPort = flag.Int("srtla-port", 5000, "Port for the SRTLA upstream (standalone/server)")

	bsPort     = flag.Int("bs-port", 9999, "Port for the Browser Source web app (client/standalone)")
	wsPort     = flag.Int("ws-port", 8888, "WebSocket server port (client/standalone)")
	udpPort    = flag.Int("udp-port", 5002, "Port for the UDP down stream (client/standalone)")
	passphrase = flag.String("passphrase", "", "Passphrase for SRT stream encryption (client/standalone)")

	verbose = flag.Bool("verbose", false, "Enable verbose logging in srtla (server/standalone)")
)

var logo = `
 ██████╗   ██████╗         ██╗ ██████╗  ██╗     
██╔════╝  ██╔═══██╗        ██║ ██╔══██╗ ██║     
██║  ███╗ ██║   ██║ █████╗ ██║ ██████╔╝ ██║     
██║   ██║ ██║   ██║ ╚════╝ ██║ ██╔══██╗ ██║     
╚██████╔╝ ╚██████╔╝        ██║ ██║  ██║ ███████╗
 ╚═════╝   ╚═════╝         ╚═╝ ╚═╝  ╚═╝ ╚══════╝
`

func getFreePort() (int, error) {
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenUDP("udp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.LocalAddr().(*net.UDPAddr).Port, nil
}

func main() {
	flag.Parse()

	fmt.Println(logo)

	switch *mode {
	case "server":
		runServerMode()
	case "client":
		runClientMode()
	case "standalone", "":
		runStandaloneMode()
	default:
		log.Fatalf("ERROR: unknown -mode '%s' (expected server|client|standalone)", *mode)
	}
}

func runServerMode() {
	if *srtPort <= 0 || *srtPort > 65535 {
		log.Fatalf("ERROR: server mode requires -srtPort (1-65535)")
	}

	log.Printf("[server mode] SRTLA listen port: %d  Output SRT: %s:%d", *srtlaPort, *srtHost, *srtPort)

	go runSrtla(uint(*srtlaPort), *srtHost, uint(*srtPort), *verbose)

	waitForSignal()
}

func runClientMode() {
	if *srtPort <= 0 || *srtPort > 65535 {
		log.Fatalf("ERROR: client mode requires -srtPort (1-65535)")
	}
	if *passphrase != "" && len(*passphrase) < 10 {
		log.Fatalf("ERROR: Passphrase must be at least 10 characters long")
	}
	if *passphrase == "" {
		log.Println("WARNING: No passphrase set. SRT stream will be unencrypted.")
	}

	fromAddr := fmt.Sprintf("srt://0.0.0.0:%d?mode=listener", *srtPort)
	if *passphrase != "" {
		fromAddr = fmt.Sprintf("srt://0.0.0.0:%d?mode=listener&passphrase=%s", *srtPort, *passphrase)
	}

	log.Printf("[client mode] Listening SRT on %s", fromAddr)

	go runBrowserSource(*bsPort)
	srtDoneChan := runSrtProxy(fromAddr, fmt.Sprintf("udp://127.0.0.1:%d", *udpPort), *wsPort)
	waitForEither(srtDoneChan)
}

func runStandaloneMode() {
	if *passphrase != "" && len(*passphrase) < 10 {
		log.Fatalf("ERROR: Passphrase must be at least 10 characters long")
	}
	if *passphrase == "" {
		log.Println("WARNING: No passphrase set. SRT stream will be unencrypted.")
	}

	internalSrtPort, err := getFreePort()
	if err != nil {
		log.Fatalf("ERROR: failed to allocate internal SRT port: %v", err)
	}

	fromAddr := fmt.Sprintf("srt://127.0.0.1:%d?mode=listener", internalSrtPort)
	if *passphrase != "" {
		fromAddr = fmt.Sprintf("srt://127.0.0.1:%d?mode=listener&passphrase=%s", internalSrtPort, *passphrase)
	}

	go runBrowserSource(*bsPort)
	go runSrtla(uint(*srtlaPort), "127.0.0.1", uint(internalSrtPort), *verbose)
	srtDoneChan := runSrtProxy(fromAddr, fmt.Sprintf("udp://127.0.0.1:%d", *udpPort), *wsPort)
	waitForEither(srtDoneChan)
}

func waitForSignal() {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
	log.Println("Shutdown signal received, exiting.")
}

func waitForEither(srtDoneChan <-chan error) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-srtDoneChan:
		if err != nil {
			log.Printf("SRT proxy exited with error: %v", err)
		} else {
			log.Println("SRT proxy exited gracefully.")
		}
	case <-signalChan:
		log.Println("Shutdown signal received, exiting.")
	}
}
