#!/bin/sh
# Runs the E2E suite in a Linux container with srtla_send (see e2e/Dockerfile),
# then replays the captured statistics through the Browser Source tests.
# Extra arguments go to `go test`, e.g. e2e/run-docker.sh -run Bonding.
# Set E2E_RACE=1 to build go-irl with the race detector.
set -eu
cd "$(dirname "$0")/.."

if [ ! -f frontend/dist/index.html ]; then
	echo "frontend/dist is missing; run: (cd frontend && npm ci && npm run build)" >&2
	exit 1
fi

rm -rf e2e/.stats
docker build -q -t go-irl-e2e -f e2e/Dockerfile e2e >/dev/null
docker run --rm \
	-v "$PWD":/work \
	-v go-irl-e2e-gomod:/go/pkg/mod \
	-v go-irl-e2e-gocache:/root/.cache/go-build \
	-e E2E_RACE \
	-e E2E_STATS_DIR=/work/e2e/.stats \
	go-irl-e2e \
	go test -tags e2e -count=1 -v "$@" ./e2e

if [ -d frontend/node_modules ]; then
	(cd frontend && E2E_STATS_DIR="$PWD/../e2e/.stats" npx vitest run e2e)
fi
