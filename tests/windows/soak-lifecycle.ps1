param(
    [Parameter(Mandatory = $true)][string] $BinDirectory,
    [Parameter(Mandatory = $true)][string] $ConfigPath,
    [Parameter(Mandatory = $true)][string] $OutputDirectory,
    [string] $TunnelName = 'wgqsoak',
    [string] $TargetAddress = '10.89.0.2',
    [ValidateRange(1, 1000)][int] $Cycles = 20,
    [switch] $UseDesktopBroker
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($TunnelName -notmatch '^wgqsoak[a-zA-Z0-9_-]*$') {
    throw 'Use a dedicated tunnel name beginning with wgqsoak.'
}
$quick = Join-Path (Resolve-Path $BinDirectory).Path 'wg-quic-quick.exe'
$core = Join-Path (Resolve-Path $BinDirectory).Path 'wg-quic.exe'
New-Item -ItemType Directory -Force $OutputDirectory | Out-Null
$OutputDirectory = (Resolve-Path $OutputDirectory).Path
$results = [Collections.Generic.List[object]]::new()
$commandIndex = 0
$lifecyclePrefix = @()
$brokerPath = ''
if ($UseDesktopBroker) {
    $broker = Get-CimInstance Win32_Service -Filter "Name='wg-quic-manager'"
    if ($null -eq $broker -or $broker.State -ne 'Running') {
        throw 'Install and start the matching desktop management service before using -UseDesktopBroker.'
    }
    if ($broker.PathName -notmatch '^(?:"([^"]+)"|(\S+))') {
        throw 'Cannot determine the desktop management service executable.'
    }
    $brokerPath = if ($Matches[1]) { $Matches[1] } else { $Matches[2] }
    if ([IO.Path]::GetFullPath($brokerPath.Replace('\\?\', '')) -ine
        [IO.Path]::GetFullPath($quick.Replace('\\?\', ''))) {
        throw 'BinDirectory must point to the running desktop management service installation.'
    }
    $lifecyclePrefix = @('desktop-client')
}

function Invoke-Recorded {
    param([string] $Exe, [string[]] $Arguments)
    $script:commandIndex++
    $stem = Join-Path $OutputDirectory ('command-{0:d4}' -f $script:commandIndex)
    $quoted = @($Arguments | ForEach-Object { '"' + $_.Replace('"', '\"') + '"' })
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $process = [Diagnostics.Process]::new()
    $process.StartInfo.FileName = $Exe
    $process.StartInfo.Arguments = $quoted -join ' '
    $process.StartInfo.UseShellExecute = $false
    $process.StartInfo.CreateNoWindow = $true
    $process.StartInfo.RedirectStandardOutput = $true
    $process.StartInfo.RedirectStandardError = $true
    $stdout = $null
    $stderr = $null
    @{ executable = $Exe; arguments = $Arguments; started_utc = [DateTime]::UtcNow.ToString('o') } |
        ConvertTo-Json | Set-Content "$stem.command.json"
    try {
        $null = $process.Start()
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(90000)) {
            $process.Kill()
            $null = $process.WaitForExit(5000)
            throw "Command timed out: $Exe $($Arguments -join ' '); logs: $stem"
        }
        if (-not $stdout.Wait(5000) -or -not $stderr.Wait(5000)) {
            throw "Command output remained open: $Exe $($Arguments -join ' ')"
        }
        $stdout.Result | Set-Content "$stem.out"
        $stderr.Result | Set-Content "$stem.err"
        if ($process.ExitCode -ne 0) {
            $detail = Get-Content "$stem.err" -Raw
            throw "Command failed ($($process.ExitCode)): $Exe $($Arguments -join ' '): $detail"
        }
        return $watch.ElapsedMilliseconds
    } finally {
        # Keep diagnostics even when timeout/output handling throws. Do not
        # dereference an unfinished task: inherited handles can keep it open.
        if ($null -ne $stdout -and $stdout.Status -eq 'RanToCompletion') {
            $stdout.Result | Set-Content "$stem.out"
        }
        if ($null -ne $stderr -and $stderr.Status -eq 'RanToCompletion') {
            $stderr.Result | Set-Content "$stem.err"
        }
        $process.Dispose()
    }
}

$os = Get-CimInstance Win32_OperatingSystem
@{ caption = $os.Caption; version = $os.Version; build = $os.BuildNumber;
   core_version = (& $core version); quick_version = (& $quick version);
   desktop_broker = [bool]$UseDesktopBroker; broker_executable = $brokerPath } |
    ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'environment.json')
$importArguments = if ($UseDesktopBroker) { @('desktop-client', 'import') } else { @('desktop-import') }
$null = Invoke-Recorded $quick ($importArguments + @($TunnelName, (Resolve-Path $ConfigPath).Path))
$ping = [Net.NetworkInformation.Ping]::new()
$failed = $false
$tunnelMayBeRunning = $false
try {
    for ($cycle = 1; $cycle -le $Cycles; $cycle++) {
        $row = [ordered]@{ cycle = $cycle; up_ms = 0; first_ping_ms = $null;
            ping_attempts = 0; route_check_ms = 0; down_ms = 0; success = $false; error = '' }
        try {
            $tunnelMayBeRunning = $true
            $row.up_ms = Invoke-Recorded $quick ($lifecyclePrefix + @('up', $TunnelName))
            $watch = [Diagnostics.Stopwatch]::StartNew()
            while ($watch.Elapsed.TotalSeconds -lt 30) {
                $row.ping_attempts++
                try {
                    if ($ping.Send($TargetAddress, 500).Status -eq 'Success') {
                        $row.first_ping_ms = $watch.ElapsedMilliseconds
                        break
                    }
                } catch { }
                Start-Sleep -Milliseconds 100
            }
            if ($null -eq $row.first_ping_ms) { throw 'Tunnel did not deliver a ping within 30 seconds after up.' }
            # More than a single fortunate packet: prove continuing delivery.
            for ($probe = 0; $probe -lt 3; $probe++) {
                if ($ping.Send($TargetAddress, 1000).Status -ne 'Success') {
                    throw 'Tunnel stopped delivering during the post-start probe.'
                }
            }
            # A reachable address on the LAN or another VPN is not proof
            # that this tunnel works. Inspect the naturally selected route;
            # filtering Find-NetRoute by our interface would hide a mistake.
            $routeWatch = [Diagnostics.Stopwatch]::StartNew()
            $selectedRoute = @(Find-NetRoute -RemoteIPAddress $TargetAddress)
            if ($selectedRoute.Count -lt 2 -or
                @($selectedRoute | Where-Object { $_.InterfaceAlias -ine $TunnelName }).Count -ne 0) {
                throw "The effective source address and route to $TargetAddress must belong to $TunnelName."
            }
            $row.route_check_ms = $routeWatch.ElapsedMilliseconds
            $row.success = $true
        } catch {
            $row.error = $_.Exception.Message
            $failed = $true
            Get-NetIPAddress | Select-Object InterfaceAlias,IPAddress,AddressState |
                ConvertTo-Json | Set-Content (Join-Path $OutputDirectory "cycle-$cycle-addresses.json")
            Get-NetRoute | Select-Object InterfaceAlias,DestinationPrefix,NextHop,RouteMetric |
                ConvertTo-Json | Set-Content (Join-Path $OutputDirectory "cycle-$cycle-routes.json")
            try { $null = Invoke-Recorded $core @('show', $TunnelName, '--json') }
            catch { $row.error += '; status: ' + $_.Exception.Message }
        } finally {
            try {
                $row.down_ms = Invoke-Recorded $quick ($lifecyclePrefix + @('down', $TunnelName))
                $tunnelMayBeRunning = $false
            }
            catch {
                $row.success = $false
                $row.error += '; down: ' + $_.Exception.Message
                $failed = $true
                try {
                    $null = Invoke-Recorded $quick @('down', $TunnelName, '--repair')
                    $tunnelMayBeRunning = $false
                } catch { $row.error += '; repair: ' + $_.Exception.Message }
            }
            $results.Add([pscustomobject]$row)
            $results.ToArray() | ConvertTo-Json -Depth 6 |
                Set-Content (Join-Path $OutputDirectory 'cycles.json')
            Write-Host ($row | ConvertTo-Json -Compress)
        }
    }
} finally {
    $ping.Dispose()
    if ($tunnelMayBeRunning) { $null = Invoke-Recorded $quick @('down', $TunnelName, '--repair') }
}
if ($failed) { throw 'One or more lifecycle rounds failed; inspect cycles.json and per-command logs.' }
