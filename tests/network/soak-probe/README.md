# Checked traffic during tunnel faults

This standalone Go tool sends a changing 64 KiB frame through TCP, reads its
echo, and compares every byte. It reconnects application sockets after a
two-second deadline but never restarts or reconfigures wg-quic. Both directions
must carry real data for a frame to count as delivered.

Build the server for the isolated Linux peer and the client for Windows:

```sh
CGO_ENABLED=0 go build -o soak-probe ./tests/network/soak-probe
GOOS=windows GOARCH=amd64 go build -o soak-probe.exe ./tests/network/soak-probe
```

Bind the server to the peer's tunnel address, then start the Windows client:

```sh
./soak-probe -listen 10.89.0.2:5202
```

```powershell
.\soak-probe.exe -connect 10.89.0.2:5202 -duration 10m > traffic.jsonl
```

The default payload limit is 2 MiB/s in each direction. This is a recovery and
data-integrity test, not a throughput ceiling benchmark. JSON lines contain
cumulative verified bytes, application reconnects, errors, the last delivery
time, and the longest gap. Exit status is nonzero for corruption, no delivery,
or no delivery during the final five seconds.

Inject loss or an outage only in the peer's isolated network namespace. Record
UTC timestamps when applying and clearing faults, then compare them with the
JSON delivery timeline. Include a clean final period long enough for recovery.
For a peer-restart test, restart both its tunnel and echo server; leave the
client's tunnel running. Expected socket errors during an outage are separate
from recovery after the fault is cleared.

The server is an unauthenticated test endpoint. Bind it to an isolated test
interface, and stop it when the test ends.
