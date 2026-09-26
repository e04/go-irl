// srtpub: reads MPEG-TS from stdin and publishes it as an SRT caller.
package main

import (
	"flag"
	"io"
	"log"
	"os"
	"time"

	srt "github.com/datarhei/gosrt"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:6000", "SRT listener (fakesrtla)")
	pass := flag.String("passphrase", "", "SRT passphrase")
	flag.Parse()
	cfg := srt.DefaultConfig()
	cfg.StreamId = "publish"
	cfg.Latency = 500 * time.Millisecond
	cfg.PeerIdleTimeout = 5 * time.Second // libsrt default, as on phones
	if *pass != "" {
		cfg.Passphrase = *pass
	}
	var conn srt.Conn
	var err error
	for {
		if conn, err = srt.Dial("srt", *addr, cfg); err == nil {
			break
		}
		log.Printf("dial: %v (retrying)", err)
		time.Sleep(time.Second)
	}
	log.Printf("connected to %s", *addr)
	buf := make([]byte, 188*7)
	for {
		if _, err := io.ReadFull(os.Stdin, buf); err != nil {
			log.Fatal(err)
		}
		if _, err := conn.Write(buf); err != nil {
			log.Fatal(err)
		}
	}
}
