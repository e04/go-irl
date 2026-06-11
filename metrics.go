package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"runtime/debug"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func buildInfoLabels() prometheus.Labels {
	labels := prometheus.Labels{"go_version": runtime.Version()}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return labels
	}
	if info.Main.Version != "" {
		labels["version"] = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			labels["revision"] = s.Value
		case "vcs.modified":
			labels["modified"] = s.Value
		}
	}
	return labels
}

var (
	totalBytesReceived   atomic.Uint64
	totalPacketsReceived atomic.Uint64
)

type srtlaCollector struct {
	groupsActive   *prometheus.Desc
	connsActive    *prometheus.Desc
	bytesTotal     *prometheus.Desc
	packetsTotal   *prometheus.Desc
	pathBytes      *prometheus.Desc
	pathPackets    *prometheus.Desc
	pathLastSeenAt *prometheus.Desc
}

func newSRTLACollector() *srtlaCollector {
	return &srtlaCollector{
		groupsActive: prometheus.NewDesc(
			"srtla_groups_active",
			"Number of active SRTLA bonding groups.",
			nil, nil,
		),
		connsActive: prometheus.NewDesc(
			"srtla_connections_active",
			"Number of active SRTLA bonding connections.",
			nil, nil,
		),
		bytesTotal: prometheus.NewDesc(
			"srtla_received_bytes_total",
			"Total bytes received on the SRTLA socket.",
			nil, nil,
		),
		packetsTotal: prometheus.NewDesc(
			"srtla_received_packets_total",
			"Total packets received on the SRTLA socket.",
			nil, nil,
		),
		pathBytes: prometheus.NewDesc(
			"srtla_path_received_bytes_total",
			"Bytes received per bonding path.",
			[]string{"group", "path"}, nil,
		),
		pathPackets: prometheus.NewDesc(
			"srtla_path_received_packets_total",
			"Packets received per bonding path.",
			[]string{"group", "path"}, nil,
		),
		pathLastSeenAt: prometheus.NewDesc(
			"srtla_path_last_seen_timestamp_seconds",
			"Unix timestamp of the last packet received on this path.",
			[]string{"group", "path"}, nil,
		),
	}
}

func (c *srtlaCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.groupsActive
	ch <- c.connsActive
	ch <- c.bytesTotal
	ch <- c.packetsTotal
	ch <- c.pathBytes
	ch <- c.pathPackets
	ch <- c.pathLastSeenAt
}

func (c *srtlaCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.bytesTotal, prometheus.CounterValue, float64(totalBytesReceived.Load()))
	ch <- prometheus.MustNewConstMetric(c.packetsTotal, prometheus.CounterValue, float64(totalPacketsReceived.Load()))

	groupsMu.RLock()
	snapshot := make([]*Group, len(groups))
	copy(snapshot, groups)
	groupsMu.RUnlock()

	var totalConns int
	for _, g := range snapshot {
		g.mu.Lock()
		groupID := hex.EncodeToString(g.id[:8])
		conns := make([]*Conn, len(g.conns))
		copy(conns, g.conns)
		g.mu.Unlock()

		totalConns += len(conns)

		for _, conn := range conns {
			addr := conn.addr.String()
			ch <- prometheus.MustNewConstMetric(c.pathBytes, prometheus.CounterValue, float64(conn.bytes.Load()), groupID, addr)
			ch <- prometheus.MustNewConstMetric(c.pathPackets, prometheus.CounterValue, float64(conn.pkts.Load()), groupID, addr)
			ch <- prometheus.MustNewConstMetric(c.pathLastSeenAt, prometheus.GaugeValue, float64(conn.lastRcvd.UnixMilli())/1000.0, groupID, addr)
		}
	}

	ch <- prometheus.MustNewConstMetric(c.groupsActive, prometheus.GaugeValue, float64(len(snapshot)))
	ch <- prometheus.MustNewConstMetric(c.connsActive, prometheus.GaugeValue, float64(totalConns))
}

func runMetricsServer(port int) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(newSRTLACollector())
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name:        "srtla_build_info",
			Help:        "Build information about the SRTLA server.",
			ConstLabels: buildInfoLabels(),
		},
		func() float64 { return 1 },
	))

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Prometheus metrics server listening on %s/metrics", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("Metrics server error: %v", err)
	}
}

func metricsRecord(c *Conn, n int) {
	totalBytesReceived.Add(uint64(n))
	totalPacketsReceived.Add(1)
	c.bytes.Add(uint64(n))
	c.pkts.Add(1)
}
