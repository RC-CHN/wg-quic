param(
    [Parameter(Mandatory = $true)][string] $BinDirectory,
    [Parameter(Mandatory = $true)][string] $ConfigPath,
    [Parameter(Mandatory = $true)][string] $OutputDirectory,
    [string] $TunnelName = 'wgqsoak',
    [string] $TargetAddress = '10.89.0.2',
    [ValidateRange(1, 1000)][int] $Cycles = 20
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
        $process.Dispose()
    }
}

$os = Get-CimInstance Win32_OperatingSystem
@{ caption = $os.Caption; version = $os.Version; build = $os.BuildNumber;
   core_version = (& $core version); quick_version = (& $quick version) } |
    ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'environment.json')
$null = Invoke-Recorded $quick @('desktop-import', $TunnelName, (Resolve-Path $ConfigPath).Path)
$ping = [Net.NetworkInformation.Ping]::new()
$failed = $false
$tunnelMayBeRunning = $false
try {
    for ($cycle = 1; $cycle -le $Cycles; $cycle++) {
        $row = [ordered]@{ cycle = $cycle; up_ms = 0; first_ping_ms = $null;
            ping_attempts = 0; down_ms = 0; success = $false; error = '' }
        try {
            $tunnelMayBeRunning = $true
            $row.up_ms = Invoke-Recorded $quick @('up', $TunnelName)
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
                $row.down_ms = Invoke-Recorded $quick @('down', $TunnelName)
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
