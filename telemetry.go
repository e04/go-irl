package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"
)

const maxTelemetryFrameSize = 1024 * 1024

var telemetryHeartbeat = []byte(`{"type":"heartbeat"}`)

func writeTelemetryFrame(w io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxTelemetryFrameSize {
		return fmt.Errorf("invalid telemetry frame size: %d", len(payload))
	}

	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	_, err := io.Copy(w, bytes.NewReader(frame))
	return err
}

func readTelemetryFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxTelemetryFrameSize {
		return nil, fmt.Errorf("invalid telemetry frame size: %d", size)
	}

	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func forwardTelemetryPayload(hub *hub, payload []byte) bool {
	var message struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &message); err != nil || message.Type != "reader" {
		return false
	}

	select {
	case hub.broadcast <- payload:
		return true
	default:
		return false
	}
}

func runStatsTelemetry(from string, hub *hub) {
	for {
		reader, err := openSrtStream(from)
		if err != nil {
			log.Printf("Failed to connect statistics channel: %v. Retrying in 5 seconds...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("Upstream statistics channel connected.")

		for {
			payload, err := readTelemetryFrame(reader)
			if err != nil {
				log.Printf("Statistics channel error: %v. Attempting to reconnect...", err)
				_ = reader.Close()
				break
			}
			forwardTelemetryPayload(hub, payload)
		}
	}
}
