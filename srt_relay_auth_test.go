package main

import (
	"errors"
	"net"
	"testing"

	srt "github.com/datarhei/gosrt"
)

// Unused request methods deliberately remain nil: unexpected calls fail the test.
type relayRequest struct {
	srt.ConnRequest
	addr       net.Addr
	stream     string
	encrypted  bool
	passphrase string
	passErr    error
}

func (r *relayRequest) RemoteAddr() net.Addr         { return r.addr }
func (r *relayRequest) StreamId() string             { return r.stream }
func (r *relayRequest) IsEncrypted() bool            { return r.encrypted }
func (r *relayRequest) SetPassphrase(p string) error { r.passphrase = p; return r.passErr }
func TestRelayConnectionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, ip, stream, password string
		encrypted, wrongKey        bool
		want                       srt.ConnType
	}{
		{"local publisher", "127.0.0.1", "", "", false, false, srt.PUBLISH},
		{"IPv6 publisher", "::1", "mobile", "", false, false, srt.PUBLISH},
		{"public publisher", "192.0.2.1", "", "", false, false, srt.REJECT},
		{"public unknown stream", "192.0.2.1", "mobile", "", false, false, srt.REJECT},
		{"subscriber", "192.0.2.1", downstreamStreamID, "", false, false, srt.SUBSCRIBE},
		{"statistics", "192.0.2.1", downstreamStatsStreamID, "", false, false, srt.SUBSCRIBE},
		{"encrypted publisher", "127.0.0.1", "", "test-passphrase", true, false, srt.PUBLISH},
		{"encrypted subscriber", "192.0.2.1", downstreamStreamID, "test-passphrase", true, false, srt.SUBSCRIBE},
		{"missing encryption", "127.0.0.1", "", "test-passphrase", false, false, srt.REJECT},
		{"wrong key", "192.0.2.1", downstreamStreamID, "test-passphrase", true, true, srt.REJECT},
		{"unexpected encryption", "127.0.0.1", "", "", true, false, srt.REJECT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &relayRequest{addr: &net.UDPAddr{IP: net.ParseIP(tc.ip)}, stream: tc.stream, encrypted: tc.encrypted}
			if tc.wrongKey {
				req.passErr = errors.New("incorrect key")
			}
			relay := newSRTRelay(tc.password)
			if got := relay.handleConnect(req); got != tc.want {
				t.Fatalf("mode = %v, want %v", got, tc.want)
			}
			if tc.encrypted && tc.password != "" && req.passphrase != tc.password {
				t.Fatal("configured key was not applied")
			}
		})
	}
}
