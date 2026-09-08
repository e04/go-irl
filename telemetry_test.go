package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
)

func TestTelemetryFrames(t *testing.T) {
	for _, size := range []int{1, 1024, maxTelemetryFrameSize} {
		payload := bytes.Repeat([]byte{0x47}, size)
		var b bytes.Buffer
		if err := writeTelemetryFrame(&b, payload); err != nil {
			t.Fatal(err)
		}
		if binary.BigEndian.Uint32(b.Bytes()[:4]) != uint32(size) {
			t.Fatal("length is not big endian")
		}
		got, err := readTelemetryFrame(&b)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("round trip size %d: %v", size, err)
		}
	}
	var b bytes.Buffer
	for _, s := range []string{"first", "second"} {
		if err := writeTelemetryFrame(&b, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"first", "second"} {
		got, err := readTelemetryFrame(&oneByteReader{&b})
		if err != nil || string(got) != s {
			t.Fatalf("fragmented frames: %q %v", got, err)
		}
	}
}

type oneByteReader struct{ io.Reader }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestTelemetryInvalidFrames(t *testing.T) {
	for _, p := range [][]byte{nil, {0}, {0, 0, 0}, {0, 0, 0, 0}, {0, 0x10, 0, 1}, {0xff, 0xff, 0xff, 0xff}, {0, 0, 0, 2, 1}} {
		if _, err := readTelemetryFrame(bytes.NewReader(p)); err == nil {
			t.Fatalf("accepted %x", p)
		}
	}
	for _, p := range [][]byte{nil, make([]byte, maxTelemetryFrameSize+1)} {
		var out bytes.Buffer
		if err := writeTelemetryFrame(&out, p); err == nil || out.Len() != 0 {
			t.Fatal("invalid frame written")
		}
	}
	if err := writeTelemetryFrame(failedWriter{}, []byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error lost: %v", err)
	}
}
func TestTelemetryFilteringAndBackpressure(t *testing.T) {
	h := &hub{broadcast: make(chan []byte, 1)}
	for _, s := range []string{"", "{", "null", "[]", `{}`, `{"type":"writer"}`, `{"type":"heartbeat"}`} {
		if forwardTelemetryPayload(h, []byte(s)) {
			t.Fatalf("forwarded %s", s)
		}
	}
	p := []byte(`{"type":"reader","stats":{}}`)
	if !forwardTelemetryPayload(h, p) {
		t.Fatal("valid message dropped")
	}
	if forwardTelemetryPayload(h, p) {
		t.Fatal("full queue accepted message")
	}
	if !bytes.Equal(<-h.broadcast, p) {
		t.Fatal("queued message changed")
	}
}
func TestLoopbackPublisherAddress(t *testing.T) {
	for _, tc := range []struct {
		addr net.Addr
		want bool
	}{
		{nil, false}, {&net.TCPAddr{IP: net.ParseIP("127.0.0.1")}, false},
		{&net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, true},
		{&net.UDPAddr{IP: net.ParseIP("::1")}, true},
		{&net.UDPAddr{IP: net.ParseIP("::ffff:127.0.0.1")}, true},
		{&net.UDPAddr{IP: net.ParseIP("192.0.2.1")}, false},
		{&net.UDPAddr{}, false},
	} {
		if got := isLoopbackAddr(tc.addr); got != tc.want {
			t.Fatalf("address %v: %v", tc.addr, got)
		}
	}
}
func FuzzTelemetryFrame(f *testing.F) {
	for _, p := range [][]byte{nil, {0, 0, 0, 1, 42}, {0xff, 0xff, 0xff, 0xff}} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p []byte) {
		got, err := readTelemetryFrame(bytes.NewReader(p))
		if err != nil {
			return
		}
		var out bytes.Buffer
		if err := writeTelemetryFrame(&out, got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), p[:4+len(got)]) {
			t.Fatal("frame roundtrip changed bytes")
		}
	})
}
