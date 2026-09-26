package main

import (
	"fmt"
	"strconv"
	"strings"
)

const minPassphraseLen = 10

type config struct {
	Mode       string
	SRTPort    int
	SRTHost    string
	SRTLAPort  int
	BSPort     int
	WSPort     int
	UDPPort    int
	Passphrase string
	Insecure   bool
	Verbose    bool
}

func configFromFlags() config {
	return config{
		Mode:       *mode,
		SRTPort:    *srtPort,
		SRTHost:    *srtHost,
		SRTLAPort:  *srtlaPort,
		BSPort:     *bsPort,
		WSPort:     *wsPort,
		UDPPort:    *udpPort,
		Passphrase: *passphrase,
		Insecure:   *insecure,
		Verbose:    *verbose,
	}
}

// normalizedMode maps the empty default to "standalone".
func (c config) normalizedMode() string {
	if c.Mode == "" {
		return "standalone"
	}
	return c.Mode
}

func validPort(p int) bool {
	return p > 0 && p <= 65535
}

type namedPort struct {
	name string
	port int
}

func (c config) requirePorts(ports ...namedPort) error {
	for _, p := range ports {
		if !validPort(p.port) {
			return fmt.Errorf("%s mode requires -%s (1-65535)", c.normalizedMode(), p.name)
		}
	}
	return nil
}

func (c config) validate() error {
	switch c.normalizedMode() {
	case "server":
		if err := c.requirePorts(namedPort{"srt-port", c.SRTPort}, namedPort{"srtla-port", c.SRTLAPort}); err != nil {
			return err
		}
		if c.SRTPort == c.SRTLAPort {
			return fmt.Errorf("-srt-port and -srtla-port must be different")
		}
	case "client":
		if err := c.requirePorts(namedPort{"srt-port", c.SRTPort}, namedPort{"bs-port", c.BSPort}, namedPort{"ws-port", c.WSPort}, namedPort{"udp-port", c.UDPPort}); err != nil {
			return err
		}
		if c.SRTHost == "" {
			return fmt.Errorf("client mode requires -srt-host")
		}
	case "standalone":
		if err := c.requirePorts(namedPort{"srtla-port", c.SRTLAPort}, namedPort{"bs-port", c.BSPort}, namedPort{"ws-port", c.WSPort}, namedPort{"udp-port", c.UDPPort}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown -mode '%s' (expected server|client|standalone)", c.Mode)
	}

	if c.Passphrase != "" && len(c.Passphrase) < minPassphraseLen {
		return fmt.Errorf("Passphrase must be at least %d characters long", minPassphraseLen)
	}
	if c.normalizedMode() == "server" {
		if err := validateServerPassphrase(c.Passphrase, c.Insecure); err != nil {
			return err
		}
	}
	return nil
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

func (c config) warnings() []string {
	if c.Passphrase != "" {
		return nil
	}
	if c.normalizedMode() == "server" {
		return []string{"-insecure set. Both SRT legs are unencrypted and anyone can publish or watch the stream."}
	}
	return []string{"No passphrase set. SRT stream will be unencrypted."}
}

func (c config) browserSourceURL() string {
	return fmt.Sprintf("http://localhost:%d/app?wsport=%d&onlineSceneName=ONLINE&offlineSceneName=OFFLINE&type=simple", c.BSPort, c.WSPort)
}

func (c config) udpOutputURL() string {
	return fmt.Sprintf("udp://127.0.0.1:%d", c.UDPPort)
}

// commandLine returns the go-irl invocation equivalent to c, listing only the
// flags relevant to its mode. The passphrase is masked.
func (c config) commandLine() string {
	m := c.normalizedMode()
	args := []string{"go-irl", "-mode", m}
	addInt := func(name string, v int) {
		args = append(args, "-"+name, strconv.Itoa(v))
	}
	switch m {
	case "server":
		addInt("srtla-port", c.SRTLAPort)
		addInt("srt-port", c.SRTPort)
	case "client":
		args = append(args, "-srt-host", c.SRTHost)
		addInt("srt-port", c.SRTPort)
		addInt("bs-port", c.BSPort)
		addInt("ws-port", c.WSPort)
		addInt("udp-port", c.UDPPort)
	case "standalone":
		addInt("srtla-port", c.SRTLAPort)
		addInt("bs-port", c.BSPort)
		addInt("ws-port", c.WSPort)
		addInt("udp-port", c.UDPPort)
	}
	if c.Passphrase != "" {
		args = append(args, "-passphrase", "****")
	}
	if c.Insecure && m == "server" {
		args = append(args, "-insecure")
	}
	if c.Verbose && m != "client" {
		args = append(args, "-verbose")
	}
	return strings.Join(args, " ")
}
