package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"golang.org/x/term"
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

	cliMode = flag.Bool("cli", false, "Use plain log output instead of the interactive terminal UI")
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
	cfg := configFromFlags()

	if *cliMode || !isInteractiveTerminal() {
		runCLI(cfg)
		return
	}
	if err := runTUI(cfg, flag.NFlag() == 0); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func runCLI(cfg config) {
	fmt.Println(logo)

	if err := cfg.validate(); err != nil {
		log.Fatalf("ERROR: %v", err)
	}
	for _, w := range cfg.warnings() {
		log.Printf("WARNING: %s", w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runMode(ctx, cfg, runHooks{}); err != nil {
		log.Fatalf("ERROR: %v", err)
	}
}

// runHooks lets the TUI observe a running mode.
type runHooks struct {
	onStats func([]byte)    // statistics broadcast to the Browser Source (client/standalone)
	onRelay func(*srtRelay) // called once the relay is listening (server)
}

// runMode starts the components for cfg's mode and blocks until ctx is done
// or a component fails. cfg must already be validated.
func runMode(ctx context.Context, cfg config, hooks runHooks) error {
	switch cfg.normalizedMode() {
	case "server":
		return runServerMode(ctx, cfg, hooks)
	case "client":
		return runClientMode(ctx, cfg, hooks)
	default:
		return runStandaloneMode(ctx, cfg, hooks)
	}
}

func runServerMode(ctx context.Context, cfg config, hooks runHooks) error {
	relay := newSRTRelay(cfg.Passphrase)
	relayAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.SRTPort))
	relayServer, err := relay.newServer(relayAddr)
	if err != nil {
		return err
	}
	defer relayServer.Shutdown()
	if hooks.onRelay != nil {
		hooks.onRelay(relay)
	}

	log.Printf("[server mode] SRTLA input UDP :%d  Downstream SRT UDP :%d", cfg.SRTLAPort, cfg.SRTPort)

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayServer.Serve()
	}()
	if err := startSrtla(uint(cfg.SRTLAPort), "127.0.0.1", uint(cfg.SRTPort), cfg.Verbose); err != nil {
		return err
	}

	select {
	case err := <-relayDone:
		if err != nil {
			return fmt.Errorf("SRT relay exited: %w", err)
		}
		log.Println("SRT relay exited gracefully.")
	case <-ctx.Done():
		log.Println("Shutdown signal received, exiting.")
	}
	return nil
}

func runClientMode(ctx context.Context, cfg config, hooks runHooks) error {
	fromAddr := makeSRTURL(cfg.SRTHost, cfg.SRTPort, "caller", cfg.Passphrase, downstreamStreamID)
	telemetryAddr := makeSRTURL(cfg.SRTHost, cfg.SRTPort, "caller", cfg.Passphrase, downstreamStatsStreamID)

	log.Printf("[client mode] Connecting to SRT server %s:%d", cfg.SRTHost, cfg.SRTPort)

	errCh := make(chan error, 1)
	if err := startBrowserSource(cfg.BSPort, errCh); err != nil {
		return err
	}
	if err := runSrtProxy(fromAddr, cfg.udpOutputURL(), cfg.WSPort, telemetryAddr, hooks.onStats); err != nil {
		return err
	}
	return waitForEither(ctx, errCh)
}

func runStandaloneMode(ctx context.Context, cfg config, hooks runHooks) error {
	internalSrtPort, err := getFreePort()
	if err != nil {
		return fmt.Errorf("failed to allocate internal SRT port: %w", err)
	}

	fromAddr := makeSRTURL("127.0.0.1", internalSrtPort, "listener", cfg.Passphrase, "")

	errCh := make(chan error, 1)
	if err := startBrowserSource(cfg.BSPort, errCh); err != nil {
		return err
	}
	if err := startSrtla(uint(cfg.SRTLAPort), "127.0.0.1", uint(internalSrtPort), cfg.Verbose); err != nil {
		return err
	}
	if err := runSrtProxy(fromAddr, cfg.udpOutputURL(), cfg.WSPort, "", hooks.onStats); err != nil {
		return err
	}
	return waitForEither(ctx, errCh)
}

func waitForEither(ctx context.Context, errCh <-chan error) error {
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Println("Shutdown signal received, exiting.")
	}
	return nil
}
