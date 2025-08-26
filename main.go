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
	bsPort     = flag.Int("bs-port", 9999, "Port for the Browser Source web app")
	toAddr     = flag.Int("udp-port", 5002, "Port for the UDP down stream")
	wsPort     = flag.Int("ws-port", 8888, "WebSocket server port")
	srtlaPort  = flag.Int("srtla-port", 5000, "Port for the SRTLA upstream")
	passphrase = flag.String("passphrase", "", "Passphrase for SRT stream encryption")
	verbose    = flag.Bool("verbose", false, "Enable verbose logging in srtla")
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

	log.Print(logo)

	if *passphrase != "" && len(*passphrase) < 10 {
		log.Fatalf("Passphrase must be at least 10 characters long")
	}

	if *passphrase == "" {
		log.Println("WARNING: No passphrase set. SRT stream will be unencrypted.")
	}

	log.SetFlags(0)

	internalSrtPort, err := getFreePort()
	if err != nil {
		log.Fatalf("Failed to find a free port for internal SRT: %v", err)
	}

	log.Printf("Using dynamic port %d for internal SRT communication.", internalSrtPort)

	fromAddr := fmt.Sprintf("srt://127.0.0.1:%d?mode=listener", internalSrtPort)
	if *passphrase != "" {
		fromAddr = fmt.Sprintf("srt://127.0.0.1:%d?mode=listener&passphrase=%s", internalSrtPort, *passphrase)
	}

	go runBrowserSource(*bsPort)

	go runSrtla(uint(*srtlaPort), "127.0.0.1", uint(internalSrtPort), *verbose)

	srtDoneChan := runSrtProxy(fromAddr, fmt.Sprintf("udp://127.0.0.1:%d", *toAddr), *wsPort)

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
