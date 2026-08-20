function Get-AvailableLifecycleUdpPort {
    [CmdletBinding()]
    param(
        [int] $Minimum = 20000,
        [int] $Maximum = 45000,
        [int] $Attempts = 256
    )

    if ($Minimum -lt 1024 -or $Maximum -gt 65535 -or
        $Minimum -gt $Maximum) {
        throw "invalid lifecycle UDP port range $Minimum-$Maximum"
    }
    if ($Attempts -le 0) {
        throw "lifecycle UDP port attempts must be positive"
    }

    # Keep the fixture outside Windows' default dynamic client range
    # (49152-65535). A PID-derived port in that range can be reassigned to an
    # unrelated WebView or runner connection between a tunnel down/up cycle.
    # An exclusive dual-stack bind also skips ports reserved by Windows or
    # already owned by another process.
    $rangeSize = $Maximum - $Minimum + 1
    $start = Get-Random -Minimum 0 -Maximum $rangeSize
    $limit = [Math]::Min($Attempts, $rangeSize)
    for ($offset = 0; $offset -lt $limit; $offset++) {
        $candidate = $Minimum + (($start + $offset) % $rangeSize)
        $socket = $null
        try {
            $socket = [Net.Sockets.Socket]::new(
                [Net.Sockets.AddressFamily]::InterNetworkV6,
                [Net.Sockets.SocketType]::Dgram,
                [Net.Sockets.ProtocolType]::Udp
            )
            $socket.DualMode = $true
            $socket.ExclusiveAddressUse = $true
            $socket.Bind([Net.IPEndPoint]::new(
                [Net.IPAddress]::IPv6Any,
                $candidate
            ))
            return $candidate
        }
        catch [Net.Sockets.SocketException] {
            continue
        }
        finally {
            if ($null -ne $socket) {
                $socket.Dispose()
            }
        }
    }
    throw (
        "could not find an exclusively bindable UDP port in " +
        "$Minimum-$Maximum after $limit attempts"
    )
}
