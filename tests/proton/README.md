# Proton Windows fixture

`windows-smoke.sh` checks Windows artifacts locally with Steam Proton. Its
default run performs three levels of verification:

1. start the Windows CLI binaries and inspect their versions;
2. parse a real `wg-quic-quick` configuration with the Windows build;
3. run a Linux-native peer against a Windows peer under Proton and compare 64
   request/reply inner IPv4 packets over the full WireGuard, QUIC, dynamic FEC,
   and Salamander data path.

Run it from the repository root:

```sh
./tests/proton/windows-smoke.sh
```

Set `WG_QUIC_PROTON_PATH` when Proton Experimental or Proton Hotfix is not in
the standard Steam location. Set `WG_QUIC_PROTON_COMPAT_DATA` to reuse a prefix.
Pass `--msi PATH` to additionally install an MSI and smoke-test the installed
Tauri renderer.

Go's Windows `net` package asks Windows to suppress UDP reset notifications.
Wine currently reports `WSAEOPNOTSUPP` for those optional ioctls, so the
interop-only Windows build uses a Go build overlay that ignores exactly that
Wine response. Release binaries are not patched. Wintun, SCM service lifecycle,
route ownership, UAC, and native WebView2 still require the Windows CI job or a
real Windows machine.
