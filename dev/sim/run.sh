#!/bin/sh
# Sends a test stream to a local go-irl through a fake SRTLA sender.
#
#   ffmpeg (test pattern) → srtpub (SRT caller) → fakesrtla (N links) → go-irl :5000
#
# Start go-irl separately (e.g. `go run . -mode standalone`); the sender
# waits for it and re-registers when it restarts. Arguments are passed to
# fakesrtla, e.g.:
#
#   dev/sim/run.sh               # 3 clean links
#   dev/sim/run.sh -chaos        # stable / random loss and outages, alternating
#   dev/sim/run.sh -links 5 -loss 0.2
#
# `pkill -USR1 fakesrtla` takes every link down (8s, or -outage).
# Set PASSPHRASE when go-irl uses -passphrase. Watch the output with
# `ffplay udp://127.0.0.1:5002` and http://127.0.0.1:9999/app.
set -eu

cd "$(dirname "$0")/../.."
command -v ffmpeg >/dev/null || { echo "ffmpeg is required" >&2; exit 1; }

bin="${TMPDIR:-/tmp}/go-irl-sim"
mkdir -p "$bin"
go build -o "$bin/fakesrtla" ./dev/sim/fakesrtla
go build -o "$bin/srtpub" ./dev/sim/srtpub

cleanup() {
	trap - EXIT INT TERM
	kill "$pub" 2>/dev/null || true
	pkill -P "$pub" 2>/dev/null || true
	pkill -f "$bin/srtpub" 2>/dev/null || true
}

# Publisher: reconnects whenever the SRT session drops, like a phone would.
(
	while :; do
		ffmpeg -hide_banner -loglevel error -re \
			-f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine=frequency=440 \
			-c:v libx264 -preset veryfast -tune zerolatency -b:v 3000k -g 60 -c:a aac \
			-f mpegts - | "$bin/srtpub" -passphrase "${PASSPHRASE:-}" || true
		echo "$(date '+%F %T') publisher disconnected, reconnecting..."
		sleep 1
	done
) >"$bin/publisher.log" 2>&1 &
pub=$!
trap cleanup EXIT INT TERM

echo "publisher log: $bin/publisher.log"
"$bin/fakesrtla" "$@"
