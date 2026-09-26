package main

import (
	"strings"
	"testing"
)

func defaultTestConfig() config {
	return config{SRTPort: 5001, SRTHost: "127.0.0.1", SRTLAPort: 5000, BSPort: 9999, WSPort: 8888, UDPPort: 5002}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*config)
		wantErr string
	}{
		{name: "standalone defaults", modify: func(c *config) {}},
		{name: "unknown mode", modify: func(c *config) { c.Mode = "relay" }, wantErr: "unknown -mode"},
		{name: "short passphrase", modify: func(c *config) { c.Passphrase = "short" }, wantErr: "at least 10"},
		{name: "server without passphrase", modify: func(c *config) { c.Mode = "server" }, wantErr: "requires -passphrase"},
		{name: "server insecure", modify: func(c *config) { c.Mode = "server"; c.Insecure = true }},
		{name: "server same ports", modify: func(c *config) { c.Mode = "server"; c.Insecure = true; c.SRTLAPort = 5001 }, wantErr: "must be different"},
		{name: "server bad port", modify: func(c *config) { c.Mode = "server"; c.Insecure = true; c.SRTPort = 70000 }, wantErr: "-srt-port"},
		{name: "client without host", modify: func(c *config) { c.Mode = "client"; c.SRTHost = "" }, wantErr: "-srt-host"},
		{name: "client bad udp port", modify: func(c *config) { c.Mode = "client"; c.UDPPort = 0 }, wantErr: "-udp-port"},
		{name: "server ignores bs port", modify: func(c *config) { c.Mode = "server"; c.Insecure = true; c.BSPort = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := defaultTestConfig()
			tt.modify(&c)
			err := c.validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestConfigCommandLine(t *testing.T) {
	c := defaultTestConfig()
	c.Mode = "client"
	c.SRTHost = "vps.example.com"
	c.Passphrase = "0123456789secret"
	c.Verbose = true

	got := c.commandLine()
	want := "go-irl -mode client -srt-host vps.example.com -srt-port 5001 -bs-port 9999 -ws-port 8888 -udp-port 5002 -passphrase ****"
	if got != want {
		t.Fatalf("commandLine() =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "secret") {
		t.Fatal("commandLine leaks the passphrase")
	}
}
