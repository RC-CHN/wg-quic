# Tunnel DNS policy probe

This isolated test responder answers A queries under
`*.wgq-native.test.invalid` with the documentation address `203.0.113.73`.
Answers have TTL zero. Other types in that test zone receive an empty answer;
names outside it receive NXDOMAIN. It never forwards queries.

Build it with `go build -o dns-probe ./tests/network/dns-probe`, then run on
the isolated tunnel peer with `./dns-probe -listen <peer-tunnel-ip>:53`.
Configure that peer address as the test tunnel's DNS server.

On Windows, query a fresh name through the **system resolver**, for example:

```powershell
Resolve-DnsName cycle1.wgq-native.test.invalid -Type A -DnsOnly -NoHostsFile
```

Do not pass `-Server`: doing so would bypass the DNS policy being tested.
Assert that the returned address is `203.0.113.73` and retain the matching
server JSON query record, tunnel configuration source and process hashes.
Use a new nonce in each query to exclude cached answers. The returned address
is a marker and is not intended to be contacted. Stop the responder and restore
the tunnel state after the test; never run this on a production DNS listener.
