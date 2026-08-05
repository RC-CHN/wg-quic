param(
    [Parameter(Mandatory = $true)]
    [string] $BinDirectory,

    [string] $TunnelName = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Invoke-Native {
    param(
        [Parameter(Mandatory = $true)]
        [string] $FilePath,

        [Parameter(Mandatory = $true)]
        [string[]] $Arguments
    )

    $output = & $FilePath @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath $($Arguments -join ' ') failed with exit code $LASTEXITCODE`n$($output | Out-String)"
    }
    return ($output | Out-String).Trim()
}

function Wait-For {
    param(
        [Parameter(Mandatory = $true)]
        [scriptblock] $Condition,

        [Parameter(Mandatory = $true)]
        [string] $Description,

        [int] $TimeoutSeconds = 30
    )

    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastError = $null
    do {
        try {
            if (& $Condition) {
                return
            }
            $lastError = $null
        }
        catch {
            # The queried Windows object may be between PnP/SCM states.
            $lastError = $_.Exception.Message
        }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)

    $detail = if ($null -ne $lastError) { ": last query failed: $lastError" } else { "" }
    throw "timed out waiting for $Description$detail"
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "the privileged Windows lifecycle test requires an Administrator token"
}

$sourceDirectory = (Resolve-Path -LiteralPath $BinDirectory).Path
foreach ($file in @("wg-quic.exe", "wg-quic-quick.exe", "wintun.dll")) {
    if (-not (Test-Path -LiteralPath (Join-Path $sourceDirectory $file) -PathType Leaf)) {
        throw "missing Windows integration binary: $file"
    }
}

if ([string]::IsNullOrWhiteSpace($TunnelName)) {
    $TunnelName = "wgqci$PID"
}

$serviceName = "wg-quic-quick@$TunnelName"
$fixtureRoot = Join-Path $env:ProgramData "wg-quic\ci\$TunnelName"
$runtimeDirectory = Join-Path $fixtureRoot "bin"
$configDirectory = Join-Path $env:ProgramData "wg-quic\interfaces"
$configPath = Join-Path $configDirectory "$TunnelName.conf"
$core = Join-Path $runtimeDirectory "wg-quic.exe"
$quick = Join-Path $runtimeDirectory "wg-quic-quick.exe"
$octet = 20 + ($PID % 200)
$localAddress = "198.18.$octet.1"
$peerAddress = "198.18.$octet.2"
$peerPrefix = "$peerAddress/32"
$endpointAddress = "192.0.2.$octet"
$endpointPrefix = "$endpointAddress/32"
$dnsServer = "192.0.2.53"
$dnsSuffix = "ci.wg-quic.invalid"
$listenPort = 52000 + ($PID % 1000)
$endpointPort = 62000 + ($PID % 1000)
$serviceStarted = $false
$endpointRoutesBefore = @(
    Get-NetRoute -AddressFamily IPv4 -DestinationPrefix $endpointPrefix -ErrorAction SilentlyContinue
)

try {
    New-Item -ItemType Directory -Force -Path $runtimeDirectory, $configDirectory | Out-Null
    Copy-Item -LiteralPath (Join-Path $sourceDirectory "wg-quic.exe") -Destination $runtimeDirectory
    Copy-Item -LiteralPath (Join-Path $sourceDirectory "wg-quic-quick.exe") -Destination $runtimeDirectory
    Copy-Item -LiteralPath (Join-Path $sourceDirectory "wintun.dll") -Destination $runtimeDirectory

    $privateKey = Invoke-Native -FilePath $core -Arguments @("genkey")
    $peerPrivateKey = Invoke-Native -FilePath $core -Arguments @("genkey")
    $peerPublicKeyOutput = $peerPrivateKey | & $core pubkey 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "derive peer public key failed with exit code $LASTEXITCODE`n$($peerPublicKeyOutput | Out-String)"
    }
    $peerPublicKey = ($peerPublicKeyOutput | Out-String).Trim()

    $configuration = @"
[Interface]
PrivateKey = $privateKey
Address = $localAddress/32
ListenPort = $listenPort
MTU = 1380
DNS = $dnsServer, $dnsSuffix

[Peer]
PublicKey = $peerPublicKey
AllowedIPs = $peerPrefix
Endpoint = ${endpointAddress}:$endpointPort
PersistentKeepalive = 1
"@
    Set-Content -LiteralPath $configPath -Value $configuration -Encoding ascii

    Write-Host (Invoke-Native -FilePath $quick -Arguments @("check", $configPath))
    Write-Host (Invoke-Native -FilePath $quick -Arguments @("up", $TunnelName))
    $serviceStarted = $true

    Wait-For -Description "the wg-quic Windows service" -Condition {
        (Get-Service -Name $serviceName -ErrorAction SilentlyContinue).Status -eq "Running"
    }
    Wait-For -Description "the Wintun adapter" -Condition {
        $null -ne (Get-NetAdapter -Name $TunnelName -ErrorAction SilentlyContinue)
    }
    Wait-For -Description "the active runtime status" -Condition {
        $candidate = (Invoke-Native -FilePath $core -Arguments @(
            "show", $TunnelName, "--json"
        )) | ConvertFrom-Json
        $candidate.interface -eq $TunnelName -and
            $candidate.state -eq "active" -and
            [int] $candidate.listen_port -eq $listenPort
    }

    $adapter = Get-NetAdapter -Name $TunnelName -ErrorAction Stop
    $service = Get-Service -Name $serviceName -ErrorAction Stop
    if ($service.Status -ne "Running") {
        throw "Windows service is $($service.Status), expected Running"
    }
    $serviceInfo = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$serviceName'" -ErrorAction Stop
    if ($serviceInfo.StartName -ne "LocalSystem") {
        throw "Windows service account is $($serviceInfo.StartName), expected LocalSystem"
    }

    Wait-For -Description "the Wintun IPv4 address" -Condition {
        $null -ne (Get-NetIPAddress -InterfaceIndex $adapter.ifIndex `
            -AddressFamily IPv4 -IPAddress $localAddress -ErrorAction SilentlyContinue)
    }
    Wait-For -Description "the Wintun IPv4 MTU" -Condition {
        (Get-NetIPInterface -InterfaceIndex $adapter.ifIndex `
            -AddressFamily IPv4 -ErrorAction Stop).NlMtuBytes -eq 1380
    }
    Wait-For -Description "the Wintun DNS server" -Condition {
        $configured = @(
            (Get-DnsClientServerAddress -InterfaceIndex $adapter.ifIndex `
                -AddressFamily IPv4 -ErrorAction Stop).ServerAddresses
        )
        $dnsServer -in $configured
    }
    Wait-For -Description "the Wintun DNS suffix" -Condition {
        (Get-DnsClient -InterfaceIndex $adapter.ifIndex `
            -ErrorAction Stop).ConnectionSpecificSuffix -eq $dnsSuffix
    }
    Wait-For -Description "the AllowedIPs route" -Condition {
        $null -ne (Get-NetRoute -InterfaceIndex $adapter.ifIndex `
            -AddressFamily IPv4 -DestinationPrefix $peerPrefix `
            -ErrorAction SilentlyContinue)
    }
    Wait-For -Description "the endpoint route pin" -Condition {
        $null -ne (
            Get-NetRoute -AddressFamily IPv4 `
                -DestinationPrefix $endpointPrefix -ErrorAction SilentlyContinue |
                Where-Object { $_.InterfaceIndex -ne $adapter.ifIndex } |
                Select-Object -First 1
        )
    }

    $ledgerPath = Join-Path $env:ProgramData "wg-quic\state\routes-v1.json"
    Wait-For -Description "the endpoint route ledger lease" -Condition {
        if (-not (Test-Path -LiteralPath $ledgerPath -PathType Leaf)) {
            return $false
        }
        $candidateLedger = Get-Content -LiteralPath $ledgerPath -Raw |
            ConvertFrom-Json
        $candidateOwnedRoutes = @($candidateLedger.routes | Where-Object {
            $_.key.destination -eq $endpointPrefix -and
            @($_.owners | Where-Object { $_.tunnel -eq $TunnelName }).Count -gt 0
        })
        $candidateOwnedRoutes.Count -eq 1
    }

    $status = (Invoke-Native -FilePath $core -Arguments @("show", $TunnelName, "--json")) |
        ConvertFrom-Json
    if ($status.interface -ne $TunnelName -or $status.state -ne "active") {
        throw "unexpected runtime status: $($status | ConvertTo-Json -Depth 8)"
    }
    if ([int] $status.listen_port -ne $listenPort) {
        throw "runtime listen port $($status.listen_port), expected $listenPort"
    }

    $address = Get-NetIPAddress -InterfaceIndex $adapter.ifIndex -AddressFamily IPv4 `
        -IPAddress $localAddress -ErrorAction Stop
    if ($address.PrefixLength -ne 32) {
        throw "Wintun address prefix is $($address.PrefixLength), expected 32"
    }

    $ipInterface = Get-NetIPInterface -InterfaceIndex $adapter.ifIndex `
        -AddressFamily IPv4 -ErrorAction Stop
    if ($ipInterface.NlMtuBytes -ne 1380) {
        throw "Wintun IPv4 MTU is $($ipInterface.NlMtuBytes), expected 1380"
    }
    $dnsAddresses = @(
        (Get-DnsClientServerAddress -InterfaceIndex $adapter.ifIndex `
            -AddressFamily IPv4 -ErrorAction Stop).ServerAddresses
    )
    if ($dnsServer -notin $dnsAddresses) {
        throw "Wintun DNS servers are $($dnsAddresses -join ', '), expected $dnsServer"
    }
    $dnsClient = Get-DnsClient -InterfaceIndex $adapter.ifIndex -ErrorAction Stop
    if ($dnsClient.ConnectionSpecificSuffix -ne $dnsSuffix) {
        throw "Wintun DNS suffix is $($dnsClient.ConnectionSpecificSuffix), expected $dnsSuffix"
    }

    $null = Get-NetRoute -InterfaceIndex $adapter.ifIndex -AddressFamily IPv4 `
        -DestinationPrefix $peerPrefix -ErrorAction Stop
    $endpointRoute = Get-NetRoute -AddressFamily IPv4 `
        -DestinationPrefix $endpointPrefix -ErrorAction Stop |
        Where-Object { $_.InterfaceIndex -ne $adapter.ifIndex } |
        Select-Object -First 1
    if ($null -eq $endpointRoute) {
        throw "endpoint route $endpointPrefix was not pinned outside Wintun"
    }

    $ledger = Get-Content -LiteralPath $ledgerPath -Raw -ErrorAction Stop | ConvertFrom-Json
    $ownedRoute = @($ledger.routes | Where-Object {
        $_.key.destination -eq $endpointPrefix -and
        @($_.owners | Where-Object { $_.tunnel -eq $TunnelName }).Count -gt 0
    })
    if ($ownedRoute.Count -ne 1) {
        throw "route ledger did not contain exactly one endpoint lease for $TunnelName"
    }

    Write-Host "privileged Windows Wintun/service/network lifecycle reached active state"

    Write-Host (Invoke-Native -FilePath $quick -Arguments @("down", $TunnelName))
    $serviceStarted = $false

    Wait-For -Description "Windows service deletion" -Condition {
        $null -eq (Get-Service -Name $serviceName -ErrorAction SilentlyContinue)
    }
    Wait-For -Description "Wintun address cleanup" -Condition {
        $null -eq (Get-NetIPAddress -AddressFamily IPv4 -IPAddress $localAddress -ErrorAction SilentlyContinue)
    }
    Wait-For -Description "AllowedIPs route cleanup" -Condition {
        $null -eq (Get-NetRoute -AddressFamily IPv4 -DestinationPrefix $peerPrefix -ErrorAction SilentlyContinue)
    }
    Wait-For -Description "Wintun adapter cleanup" -Condition {
        $null -eq (Get-NetAdapter -Name $TunnelName -ErrorAction SilentlyContinue)
    }

    if ($endpointRoutesBefore.Count -eq 0) {
        Wait-For -Description "endpoint pin cleanup" -Condition {
            $null -eq (Get-NetRoute -AddressFamily IPv4 -DestinationPrefix $endpointPrefix -ErrorAction SilentlyContinue)
        }
    }

    if (Test-Path -LiteralPath $ledgerPath) {
        $ledger = Get-Content -LiteralPath $ledgerPath -Raw | ConvertFrom-Json
        $remainingOwner = @($ledger.routes.owners | Where-Object { $_.tunnel -eq $TunnelName })
        if ($remainingOwner.Count -ne 0) {
            throw "route ledger retained an owner for $TunnelName after shutdown"
        }
    }

    Write-Host "privileged Windows Wintun/service/network lifecycle cleanup passed"
}
finally {
    if ($serviceStarted -or $null -ne (Get-Service -Name $serviceName -ErrorAction SilentlyContinue)) {
        try {
            $null = Invoke-Native -FilePath $quick -Arguments @("down", $TunnelName)
        }
        catch {
            Write-Warning "normal tunnel cleanup failed: $_"
            Stop-Service -Name $serviceName -Force -ErrorAction SilentlyContinue
            & sc.exe delete $serviceName | Out-Null
        }
    }

    Get-NetRoute -AddressFamily IPv4 -DestinationPrefix $peerPrefix -ErrorAction SilentlyContinue |
        Where-Object {
            $adapter = Get-NetAdapter -Name $TunnelName -ErrorAction SilentlyContinue
            $null -ne $adapter -and $_.InterfaceIndex -eq $adapter.ifIndex
        } |
        Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue
    Get-NetIPAddress -AddressFamily IPv4 -IPAddress $localAddress -ErrorAction SilentlyContinue |
        Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue

    Remove-Item -LiteralPath $configPath -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $fixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
}
