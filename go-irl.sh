#!/bin/bash

set -m

GO_SRTLA_ARGS=(
    -srtla_port "5000"
    -srt_hostname "127.0.0.1"
    -srt_port "5001"
)

# Passphrase is optional, if you want to use it, uncomment the line below and set the passphrase
# PASSPHRASE="1234567890"
SRT_REPORTER_FROM="srt://:5001?mode=listener"
if [ -n "${PASSPHRASE}" ]; then
    SRT_REPORTER_FROM="${SRT_REPORTER_FROM}&passphrase=${PASSPHRASE}"
fi

SRT_LIVE_REPORTER_ARGS=(
    -from "${SRT_REPORTER_FROM}"
    -to "udp://127.0.0.1:5002"
    -wsport "8888"
)

OBS_SRT_BRIDGE_ARGS=(
    -port "9999"
)

GO_SRTLA_BIN="./go-srtla"
SRT_LIVE_REPORTER_BIN="./srt-live-reporter"
OBS_SRT_BRIDGE_BIN="./obs-srt-bridge"

# --- Script Logic (Do not edit below this line unless you know what you are doing) ---
pids=()

cleanup() {
    echo ""
    echo "[Manager] Interrupt signal received. Shutting down all managed processes..."
    for pid in "${pids[@]}"; do
        if ps -p "$pid" > /dev/null; then
            echo "[Manager] Stopping process group with PGID $pid..."
            kill -TERM -- "-$pid" 2>/dev/null
        fi
    done
    echo "[Manager] All processes stopped. Exiting."
    exit 0
}

# Trap Ctrl+C (SIGINT) and termination signals (SIGTERM) to run the cleanup function.
trap cleanup INT TERM

start_and_monitor() {
    local name="$1"
    shift
    local cmd_and_args=("$@")

    while true; do
        if [ ! -x "${cmd_and_args[0]}" ]; then
            echo -e "[Manager] ERROR: Binary for '$name' not found or not executable at '${cmd_and_args[0]}'.
            Please ensure the binary exists and has execute permissions (e.g., 'chmod +x ${cmd_and_args[0]}').
            You can download the latest pre-built binary from: https://github.com/e04/$name/releases/"
            return 1
        fi

        echo "[Manager] Starting '$name'..."
        
        "${cmd_and_args[@]}" 2>&1 | while IFS= read -r line || [ -n "$line" ]; do
            printf '[%s:%s] %s\n' "$name" "$(date +'%Y-%m-%d %H:%M:%S')" "$line"
        done
        
        exit_code=${PIPESTATUS[0]}

        if [ "$exit_code" -eq 137 ]; then
            echo "[Manager] Process '$name' was killed (SIGKILL). Shutting down manager and child processes."
            kill -TERM "$PPID" 2>/dev/null
            exit 1
        fi

        echo "[Manager] Process '$name' exited with code ${exit_code}. Restarting in 5 seconds..."
        sleep 5
    done
}

# --- Main Execution ---

echo -e "\n\n
  ██████╗   ██████╗         ██╗ ██████╗  ██╗     
 ██╔════╝  ██╔═══██╗        ██║ ██╔══██╗ ██║     
 ██║  ███╗ ██║   ██║ █████╗ ██║ ██████╔╝ ██║     
 ██║   ██║ ██║   ██║ ╚════╝ ██║ ██╔══██╗ ██║     
 ╚██████╔╝ ╚██████╔╝        ██║ ██║  ██║ ███████╗
  ╚═════╝   ╚═════╝         ╚═╝ ╚═╝  ╚═╝ ╚══════╝

"


start_and_monitor "go-srtla" "${GO_SRTLA_BIN}" "${GO_SRTLA_ARGS[@]}" &
pids+=($!)

start_and_monitor "srt-live-reporter" "${SRT_LIVE_REPORTER_BIN}" "${SRT_LIVE_REPORTER_ARGS[@]}" &
pids+=($!)

start_and_monitor "obs-srt-bridge" "${OBS_SRT_BRIDGE_BIN}" "${OBS_SRT_BRIDGE_ARGS[@]}" &
pids+=($!)

echo "[Manager] All processes have been started and are being monitored."

wait "${pids[@]}"
