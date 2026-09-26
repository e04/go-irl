# End-to-end tests

These tests run the real `go-irl` binary against the real SRTLA sender from
[irlserver/srtla](https://github.com/irlserver/srtla) (`srtla_send`):

```
SRT caller (gosrt) → srtla_send (3 links: 127.0.0.1-3) → link proxy → go-irl → UDP sink (OBS)
                                                                             → WebSocket (Browser Source)
```

The link proxy keeps each bonded link on its own socket and can drop packets
per link, so the tests can take a link down or make every link lossy.

| Test | What it covers |
| --- | --- |
| `TestStandaloneBonding` | Registration of every link, traffic on all links, byte-exact in-order output, WebSocket stats values and 1s cadence, `/app` |
| `TestStandaloneIdleLinksStayUp` | Keepalive echo keeps links up before going live |
| `TestStandaloneLinkFailover` | A dead link loses no data, times out in go-irl and rejoins the same group |
| `TestStandaloneLossReflectedInStats` | 30% loss on every link shows up in `PktRecvLossRate` and clears afterwards |
| `TestStandaloneSenderReconnect` | Stats stop while the sender is gone (offline scene) and a new session is picked up |
| `TestServerClientRelay` | Server/client relay: stream and upstream stats reach the client; wrong or missing passphrases and a second publisher are rejected |
| `TestServerPublisherReconnect` | A reconnecting phone reaches the client without restarting either side |

Statistics received over the WebSocket are saved to `E2E_STATS_DIR`, and
`frontend/e2e/stats.test.tsx` replays them through the Browser Source: schema,
connection quality, scene switching and the values shown.

## Running

`srtla_send` only builds on Linux. On any host with Docker:

```bash
e2e/run-docker.sh
```

Arguments are passed to `go test`, e.g. `e2e/run-docker.sh -run Failover`.
`E2E_RACE=1` builds go-irl with the race detector. `frontend/dist` must be
built first.

On Linux, build `srtla_send` yourself and point `SRTLA_SEND` at it:

```bash
SRTLA_SEND=/path/to/srtla_send E2E_STATS_DIR=$PWD/e2e/.stats go test -tags e2e -v ./e2e
```

Build `srtla_send` with `-DCMAKE_BUILD_TYPE=Debug`: it reads the clock inside
`assert()`, so a release build never registers a link. The pinned srtla
commit is `SRTLA_REF` in `e2e/Dockerfile` and `.github/workflows/test.yml`.
