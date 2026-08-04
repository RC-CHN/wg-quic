# wg-quic Windows test bundle

This directory is self-contained for one CPU architecture. Keep
`wg-quic.exe`, `wg-quic-quick.exe`, and the official `wintun.dll` together.
Run the commands below from an elevated PowerShell terminal.

## First LAN test

Use two unused tunnel addresses, not addresses from the physical LAN. The
example uses `10.203.0.2/32` for Windows and `10.203.0.1/32` for the peer.
Replace `WINDOWS_LAN_IP`, `PEER_LAN_IP`, and all keys with real values.

Create `%ProgramData%\wg-quic\interfaces\wg0.conf`:

```ini
[Interface]
PrivateKey = WINDOWS_PRIVATE_KEY
Address = 10.203.0.2/32
ListenPort = 51820
MTU = 1380

[Peer]
PublicKey = PEER_PUBLIC_KEY
AllowedIPs = 10.203.0.1/32
Endpoint = PEER_LAN_IP:51820
PersistentKeepalive = 5
```

Configure the Linux or FreeBSD peer with the inverse addresses and the Windows
LAN endpoint:

```ini
[Interface]
PrivateKey = PEER_PRIVATE_KEY
Address = 10.203.0.1/32
ListenPort = 51820
MTU = 1380

[Peer]
PublicKey = WINDOWS_PUBLIC_KEY
AllowedIPs = 10.203.0.2/32
Endpoint = WINDOWS_LAN_IP:51820
PersistentKeepalive = 5
```

If a `PresharedKey` is used, put the same value in both peer sections. It is
also mixed into the Salamander key derivation.

Allow the outer UDP port on Windows:

```powershell
New-NetFirewallRule `
  -DisplayName "wg-quic test UDP 51820" `
  -Direction Inbound -Action Allow -Protocol UDP -LocalPort 51820
```

Start with debug mode so errors remain visible and are also written to a
timestamped log under `%ProgramData%\wg-quic\logs`:

```powershell
.\wg-quic-quick.exe check wg0
.\wg-quic-quick.exe debug wg0
```

From a second elevated terminal:

```powershell
.\wg-quic.exe show wg0
ping 10.203.0.1
Get-NetAdapter -Name wg0
Get-NetRoute -InterfaceAlias wg0
```

Debug logs include version/elevation information, read-only adapter, IP, DNS,
and route snapshots, host-policy lifecycle, Wintun/WireGuard events, QUIC
session state, and five-second traffic/FEC counters. Private keys and preshared
keys are omitted; WireGuard may print an abbreviated peer public-key identifier
for correlation. Network addresses are included.

After reproducing a problem, stop debug mode with Ctrl+C and collect the newest
log:

```powershell
$log = Get-ChildItem "$env:ProgramData\wg-quic\logs\wg0-debug-*.log" |
  Sort-Object LastWriteTime -Descending |
  Select-Object -First 1
Compress-Archive -Path $log.FullName -DestinationPath .\wg-quic-debug.zip
```

After foreground testing succeeds, exercise the per-tunnel Windows service:

```powershell
.\wg-quic-quick.exe up wg0
.\wg-quic.exe show wg0
.\wg-quic-quick.exe down wg0
```

Remove the temporary firewall rule after testing:

```powershell
Remove-NetFirewallRule -DisplayName "wg-quic test UDP 51820"
```

Start with the two `/32` AllowedIPs above. Test `0.0.0.0/0` only after the
split tunnel passes; a default route affects all Windows traffic.

## Current validation boundary

Hosted Windows CI runs the Named Pipe, QUIC/ArmorBind, host-network plan, and
SCM state-machine tests and smokes both CLIs. It does not mutate the hosted
runner's adapters or routes. This LAN procedure is the first privileged Wintun
and Windows network-policy integration test.
