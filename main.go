package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

var (
	mode    = flag.String("mode", "", "Operation mode: server | client | standalone (default: standalone)")
	srtPort = flag.Int("srt-port", 5001, "SRT downstream port (server/client)")
	srtHost = flag.String("srt-host", "127.0.0.1", "Remote SRT server host address (client mode)")

	srtlaPort = flag.Int("srtla-port", 5000, "Port for the SRTLA upstream (standalone/server)")

	bsPort     = flag.Int("bs-port", 9999, "Port for the Browser Source web app (client/standalone)")
	wsPort     = flag.Int("ws-port", 8888, "WebSocket server port (client/standalone)")
	udpPort    = flag.Int("udp-port", 5002, "Port for the UDP down stream (client/standalone)")
	passphrase = flag.String("passphrase", "", "Passphrase for SRT stream encryption")
	insecure   = flag.Bool("insecure", false, "Allow server mode to run without a passphrase (anyone can publish or watch the stream)")

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

func makeSRTURL(host string, port int, mode, passphrase, streamID string) string {
	u := url.URL{
		Scheme: "srt",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}
	query := u.Query()
	if mode != "" {
		query.Set("mode", mode)
	}
	if passphrase != "" {
		query.Set("passphrase", passphrase)
	}
	if streamID != "" {
		query.Set("streamid", streamID)
	}
	u.RawQuery = query.Encode()
	return u.String()
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

// validateServerPassphrase rejects an empty passphrase in server mode unless
// explicitly allowed. The relay is publicly reachable and the downstream stream
// ID is fixed, so without encryption anyone could watch or hijack the stream.
func validateServerPassphrase(passphrase string, allowInsecure bool) error {
	if passphrase == "" && !allowInsecure {
		return fmt.Errorf("server mode requires -passphrase (use -insecure to run without encryption)")
	}
	return nil
}

func runServerMode() {
	if *srtPort <= 0 || *srtPort > 65535 {
		log.Fatalf("ERROR: server mode requires -srt-port (1-65535)")
	}
	if *srtlaPort <= 0 || *srtlaPort > 65535 {
		log.Fatalf("ERROR: server mode requires -srtla-port (1-65535)")
	}
	if *srtPort == *srtlaPort {
		log.Fatalf("ERROR: -srt-port and -srtla-port must be different")
	}
	if *passphrase != "" && len(*passphrase) < 10 {
		log.Fatalf("ERROR: Passphrase must be at least 10 characters long")
	}
	if err := validateServerPassphrase(*passphrase, *insecure); err != nil {
		log.Fatalf("ERROR: %v", err)
	}
	if *passphrase == "" {
		log.Println("WARNING: -insecure set. Both SRT legs are unencrypted and anyone can publish or watch the stream.")
	}

	relay := newSRTRelay(*passphrase)
	relayAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(*srtPort))
	relayServer, err := relay.newServer(relayAddr)
	if err != nil {
		log.Fatalf("ERROR: %v", err)
	}

	log.Printf("[server mode] SRTLA input UDP :%d  Downstream SRT UDP :%d", *srtlaPort, *srtPort)

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayServer.Serve()
	}()
	go runSrtla(uint(*srtlaPort), "127.0.0.1", uint(*srtPort), *verbose)

	waitForRelay(relayDone)
	relayServer.Shutdown()
}

func runClientMode() {
	if *srtPort <= 0 || *srtPort > 65535 {
		log.Fatalf("ERROR: client mode requires -srt-port (1-65535)")
	}
	if *srtHost == "" {
		log.Fatalf("ERROR: client mode requires -srt-host")
	}
	if *passphrase != "" && len(*passphrase) < 10 {
		log.Fatalf("ERROR: Passphrase must be at least 10 characters long")
	}
	if *passphrase == "" {
		log.Println("WARNING: No passphrase set. SRT stream will be unencrypted.")
	}

	fromAddr := makeSRTURL(*srtHost, *srtPort, "caller", *passphrase, downstreamStreamID)
	telemetryAddr := makeSRTURL(*srtHost, *srtPort, "caller", *passphrase, downstreamStatsStreamID)

	log.Printf("[client mode] Connecting to SRT server %s:%d", *srtHost, *srtPort)

	go runBrowserSource(*bsPort)
	srtDoneChan := runSrtProxy(fromAddr, fmt.Sprintf("udp://127.0.0.1:%d", *udpPort), *wsPort, telemetryAddr)
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

	fromAddr := makeSRTURL("127.0.0.1", internalSrtPort, "listener", *passphrase, "")

	go runBrowserSource(*bsPort)
	go runSrtla(uint(*srtlaPort), "127.0.0.1", uint(internalSrtPort), *verbose)
	srtDoneChan := runSrtProxy(fromAddr, fmt.Sprintf("udp://127.0.0.1:%d", *udpPort), *wsPort, "")
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

func waitForRelay(relayDone <-chan error) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-relayDone:
		if err != nil {
			log.Printf("SRT relay exited with error: %v", err)
		} else {
			log.Println("SRT relay exited gracefully.")
		}
	case <-signalChan:
		log.Println("Shutdown signal received, exiting.")
	}
}
