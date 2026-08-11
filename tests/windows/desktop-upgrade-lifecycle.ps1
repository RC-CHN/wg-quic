param(
    [Parameter(Mandatory = $true)]
    [string] $PreviousInstaller,
    [Parameter(Mandatory = $true)]
    [string] $CurrentInstaller
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

function Wait-For {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Description,
        [Parameter(Mandatory = $true)]
        [scriptblock] $Condition,
        [int] $TimeoutSeconds = 60
    )

    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastError = $null
    do {
        try {
            if (& $Condition) {
                return
            }
            $lastError = "condition returned false"
        }
        catch {
            $lastError = $_.Exception.Message
        }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "timed out waiting for ${Description}: $lastError"
}

function Wait-ProcessExit {
    param(
        [Parameter(Mandatory = $true)]
        [System.Diagnostics.Process] $Process,
        [Parameter(Mandatory = $true)]
        [string] $Description,
        [int] $TimeoutSeconds = 180
    )

    Wait-For -Description $Description -TimeoutSeconds $TimeoutSeconds `
        -Condition {
            $Process.Refresh()
            $Process.HasExited
        }
    $Process.Refresh()
    return $Process.ExitCode
}

function Invoke-Native {
    param(
        [Parameter(Mandatory = $true)]
        [string] $FilePath,
        [Parameter(Mandatory = $true)]
        [string[]] $Arguments
    )

    $output = @(& $FilePath @Arguments 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw (
            "$FilePath $($Arguments -join ' ') failed with exit code " +
            "$LASTEXITCODE`n$($output | Out-String)"
        )
    }
    return ($output | Out-String).Trim()
}

function Invoke-Msi {
    param(
        [Parameter(Mandatory = $true)]
        [ValidateSet("install", "uninstall")]
        [string] $Action,
        [Parameter(Mandatory = $true)]
        [string] $Path,
        [Parameter(Mandatory = $true)]
        [string] $LogPath
    )

    $switch = if ($Action -eq "install") { "/i" } else { "/x" }
    $process = Start-Process -FilePath "msiexec.exe" -ArgumentList @(
        $switch,
        "`"$Path`"",
        "/qn",
        "/norestart",
        "/l*v",
        "`"$LogPath`""
    ) -PassThru
    $exitCode = Wait-ProcessExit -Process $process `
        -Description "the MSI $Action of $(Split-Path -Leaf $Path)"
    if ($exitCode -notin @(0, 3010)) {
        throw "MSI $Action exited with code $exitCode"
    }
}

function Find-InstalledExecutable {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Name
    )

    $candidate = Get-ChildItem -LiteralPath $installRoot -Recurse -File `
        -Filter $Name -ErrorAction Stop |
        Select-Object -First 1
    if ($null -eq $candidate) {
        throw "installed executable $Name was not found under $installRoot"
    }
    return $candidate.FullName
}

function Service-Executable {
    param(
        [Parameter(Mandatory = $true)]
        [string] $PathName
    )

    if ($PathName -match '^"([^"]+)"') {
        return $Matches[1]
    }
    if ($PathName -match '^(\S+)') {
        return $Matches[1]
    }
    throw "cannot parse service executable from $PathName"
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator
)) {
    throw "the desktop upgrade lifecycle requires an Administrator token"
}

$previousInstallerPath = (Resolve-Path -LiteralPath $PreviousInstaller).Path
$currentInstallerPath = (Resolve-Path -LiteralPath $CurrentInstaller).Path
$installRoot = Join-Path $env:ProgramFiles "wg-quic"
$programDataRoot = Join-Path $env:ProgramData "wg-quic"
$fixtureRoot = Join-Path $env:TEMP "wg-quic-upgrade-$PID"
$previousLog = Join-Path $fixtureRoot "previous-install.log"
$upgradeLog = Join-Path $fixtureRoot "current-upgrade.log"
$uninstallLog = Join-Path $fixtureRoot "current-uninstall.log"
$tunnelName = "wgqupgrade$PID"
$serviceName = "wg-quic-quick@$tunnelName"
$managerServiceName = "wg-quic-manager"
$sourceConfig = Join-Path $fixtureRoot "$tunnelName.conf"
$installedConfig = Join-Path (
    Join-Path $programDataRoot "interfaces"
) "$tunnelName.conf"
$previousInstalled = $false
$currentInstalled = $false
$oldStagedQuick = $null
$oldStagedCore = $null
$currentQuick = $null

try {
    New-Item -ItemType Directory -Force -Path $fixtureRoot | Out-Null
    Invoke-Msi -Action install -Path $previousInstallerPath `
        -LogPath $previousLog
    $previousInstalled = $true

    $previousQuick = Find-InstalledExecutable "wg-quic-quick.exe"
    $previousCore = Find-InstalledExecutable "wg-quic.exe"
    $privateKey = Invoke-Native -FilePath $previousCore `
        -Arguments @("genkey")
    $peerPrivateKey = Invoke-Native -FilePath $previousCore `
        -Arguments @("genkey")
    $peerPublicKey = @($peerPrivateKey | & $previousCore pubkey 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw "derive upgrade peer public key failed"
    }
    $peerPublicKey = ($peerPublicKey | Out-String).Trim()
    $listenPort = 54000 + ($PID % 1000)
    $endpointPort = 64000 + ($PID % 1000)
    @"
[Interface]
PrivateKey = $privateKey
Address = 198.21.0.1/32
ListenPort = $listenPort
MTU = 1380

[Peer]
PublicKey = $peerPublicKey
AllowedIPs = 198.21.0.2/32
Endpoint = 192.0.2.202:$endpointPort
PersistentKeepalive = 1
"@ | Set-Content -LiteralPath $sourceConfig -Encoding ascii

    Write-Host (Invoke-Native -FilePath $previousQuick -Arguments @(
        "desktop-import", $tunnelName, $sourceConfig
    ))
    $legacyConfigHash = (
        Get-FileHash -LiteralPath $installedConfig -Algorithm SHA256
    ).Hash
    Write-Host (Invoke-Native -FilePath $previousQuick -Arguments @(
        "up", $tunnelName
    ))
    Wait-For -Description "the v0.2.0 tunnel service" -Condition {
        (Get-Service -Name $serviceName -ErrorAction SilentlyContinue).Status `
            -eq "Running"
    }
    Wait-For -Description "the v0.2.0 Wintun adapter" -Condition {
        $null -ne (Get-NetAdapter -Name $tunnelName `
            -ErrorAction SilentlyContinue)
    }

    $oldService = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$serviceName'" -ErrorAction Stop
    $oldProcessId = [uint32] $oldService.ProcessId
    if ($oldProcessId -eq 0) {
        throw "the active v0.2.0 tunnel service has no process ID"
    }
    $oldStagedQuick = Service-Executable $oldService.PathName
    $oldStagedCore = Join-Path (
        Split-Path -Parent $oldStagedQuick
    ) "wg-quic.exe"
    $oldStatus = Invoke-Native -FilePath $oldStagedCore -Arguments @(
        "show", $tunnelName, "--json"
    ) | ConvertFrom-Json
    if ($oldStatus.interface -ne $tunnelName -or
        $oldStatus.state -ne "up") {
        throw "v0.2.0 staged runtime did not report the active tunnel"
    }
    $rootIdentityBefore = Invoke-Native -FilePath "fsutil.exe" `
        -Arguments @("file", "queryFileID", $programDataRoot)
    $quarantineBefore = @(
        Get-ChildItem -LiteralPath $env:ProgramData -Directory `
            -Filter ".wg-quic-quarantine-*" -ErrorAction SilentlyContinue |
            ForEach-Object { $_.FullName } |
            Sort-Object
    )

    Invoke-Msi -Action install -Path $currentInstallerPath `
        -LogPath $upgradeLog
    $currentInstalled = $true
    $previousInstalled = $false
    Wait-For -Description "the upgraded management service" `
        -TimeoutSeconds 60 -Condition {
            (Get-Service -Name $managerServiceName `
                -ErrorAction SilentlyContinue).Status -eq "Running"
        }
    $currentManager = Find-InstalledExecutable "wg-quic-manager.exe"
    $managerService = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$managerServiceName'" -ErrorAction Stop
    $expectedManagerCommand = "`"$currentManager`" broker-service"
    if ($managerService.State -ne "Running" -or
        $managerService.StartMode -ne "Auto" -or
        $managerService.StartName -ne "LocalSystem" -or
        -not $managerService.PathName.Equals(
            $expectedManagerCommand,
            [StringComparison]::OrdinalIgnoreCase
        )) {
        throw (
            "the upgraded manager service has stale wiring: " +
            "state=$($managerService.State) " +
            "start=$($managerService.StartMode) " +
            "account=$($managerService.StartName) " +
            "path=$($managerService.PathName) " +
            "expected=$expectedManagerCommand"
        )
    }
    $upgradedService = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$serviceName'" -ErrorAction Stop
    if ($upgradedService.State -ne "Running" -or
        [uint32] $upgradedService.ProcessId -ne $oldProcessId) {
        throw (
            "the MSI upgrade interrupted or replaced the active tunnel: " +
            "before=$oldProcessId after=$($upgradedService.ProcessId) " +
            "state=$($upgradedService.State)"
        )
    }
    $upgradedStatus = Invoke-Native -FilePath $oldStagedCore -Arguments @(
        "show", $tunnelName, "--json"
    ) | ConvertFrom-Json
    if ($upgradedStatus.interface -ne $tunnelName -or
        $upgradedStatus.state -ne "up") {
        throw "the active staged runtime lost status across the MSI upgrade"
    }
    if (-not (Test-Path -LiteralPath $installedConfig -PathType Leaf)) {
        throw "the MSI upgrade removed the existing tunnel configuration"
    }
    $rootIdentityAfter = Invoke-Native -FilePath "fsutil.exe" `
        -Arguments @("file", "queryFileID", $programDataRoot)
    if ($rootIdentityAfter -ne $rootIdentityBefore) {
        throw "the MSI upgrade replaced ProgramData while a tunnel was active"
    }
    $quarantineAfter = @(
        Get-ChildItem -LiteralPath $env:ProgramData -Directory `
            -Filter ".wg-quic-quarantine-*" -ErrorAction SilentlyContinue |
            ForEach-Object { $_.FullName } |
            Sort-Object
    )
    if (@(Compare-Object $quarantineBefore $quarantineAfter).Count -ne 0) {
        throw "the MSI upgrade migrated ProgramData before tunnel shutdown"
    }

    $currentQuick = Find-InstalledExecutable "wg-quic-quick.exe"
    $brokerStatus = Invoke-Native -FilePath $currentQuick -Arguments @(
        "desktop-broker-status"
    ) | ConvertFrom-Json
    if ($brokerStatus.status -ne "ready" -or
        $brokerStatus.service_name -ne $managerServiceName -or
        [int] $brokerStatus.protocol_version -ne 1) {
        throw "the upgraded management broker is not ready"
    }
    $blockedCheck = @(& $currentQuick desktop-client check $tunnelName 2>&1)
    if ($LASTEXITCODE -eq 0) {
        throw "the upgraded broker migrated ProgramData while a tunnel was active"
    }
    if (($blockedCheck | Out-String) -notmatch
        "migration requires all tunnels to be inactive") {
        throw (
            "active-tunnel migration returned an unexpected error: " +
            ($blockedCheck | Out-String)
        )
    }
    $serviceAfterCheck = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$serviceName'" -ErrorAction Stop
    if ([uint32] $serviceAfterCheck.ProcessId -ne $oldProcessId) {
        throw "blocked migration interrupted the active upgraded tunnel"
    }
    $rootIdentityBlocked = Invoke-Native -FilePath "fsutil.exe" `
        -Arguments @("file", "queryFileID", $programDataRoot)
    if ($rootIdentityBlocked -ne $rootIdentityBefore) {
        throw "blocked migration replaced ProgramData while the tunnel was active"
    }
    $quarantineBlocked = @(
        Get-ChildItem -LiteralPath $env:ProgramData -Directory `
            -Filter ".wg-quic-quarantine-*" -ErrorAction SilentlyContinue |
            ForEach-Object { $_.FullName } |
            Sort-Object
    )
    if (@(Compare-Object $quarantineBefore $quarantineBlocked).Count -ne 0) {
        throw "blocked migration created a quarantine while the tunnel was active"
    }

    Write-Host (Invoke-Native -FilePath $currentQuick -Arguments @(
        "down", $tunnelName
    ))
    Wait-For -Description "the upgraded legacy service teardown" `
        -Condition {
            $null -eq (Get-Service -Name $serviceName `
                -ErrorAction SilentlyContinue)
        }
    Wait-For -Description "the upgraded legacy adapter teardown" `
        -Condition {
            $null -eq (Get-NetAdapter -Name $tunnelName `
                -ErrorAction SilentlyContinue)
        }

    $check = Invoke-Native -FilePath $currentQuick -Arguments @(
        "desktop-client", "check", $tunnelName
    )
    if ($check -notmatch "configuration is valid for wg-quic-quick") {
        throw "the upgraded broker did not migrate and validate the profile"
    }
    if (-not (Test-Path -LiteralPath $installedConfig -PathType Leaf)) {
        throw "post-shutdown ProgramData migration lost the existing profile"
    }
    $migratedConfigHash = (
        Get-FileHash -LiteralPath $installedConfig -Algorithm SHA256
    ).Hash
    if ($migratedConfigHash -ne $legacyConfigHash) {
        throw "post-shutdown ProgramData migration changed the existing profile"
    }
    $migratedConfigAcl = Get-Acl -LiteralPath $installedConfig
    $migratedOwnerSid = (
        [Security.Principal.NTAccount] $migratedConfigAcl.Owner
    ).Translate([Security.Principal.SecurityIdentifier]).Value
    if (-not $migratedConfigAcl.AreAccessRulesProtected -or
        $migratedOwnerSid -ne "S-1-5-18") {
        throw (
            "post-shutdown migrated profile is not protected/System-owned: " +
            "protected=$($migratedConfigAcl.AreAccessRulesProtected) " +
            "owner=$migratedOwnerSid"
        )
    }
    $rootIdentityMigrated = Invoke-Native -FilePath "fsutil.exe" `
        -Arguments @("file", "queryFileID", $programDataRoot)
    if ($rootIdentityMigrated -eq $rootIdentityBefore) {
        throw "post-shutdown ProgramData migration did not replace legacy root"
    }
    $quarantineMigrated = @(
        Get-ChildItem -LiteralPath $env:ProgramData -Directory `
            -Filter ".wg-quic-quarantine-*" -ErrorAction SilentlyContinue |
            ForEach-Object { $_.FullName } |
            Sort-Object
    )
    $newQuarantines = @(
        Compare-Object $quarantineBefore $quarantineMigrated |
            Where-Object { $_.SideIndicator -eq "=>" }
    )
    if ($newQuarantines.Count -ne 1) {
        throw (
            "post-shutdown migration created " +
            "$($newQuarantines.Count) quarantine roots; expected one"
        )
    }

    Write-Host (Invoke-Native -FilePath $currentQuick -Arguments @(
        "desktop-client", "up", $tunnelName
    ))
    Wait-For -Description "the current broker-safe service" -Condition {
        (Get-Service -Name $serviceName -ErrorAction SilentlyContinue).Status `
            -eq "Running"
    }
    $brokerSafeService = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$serviceName'" -ErrorAction Stop
    if ($brokerSafeService.PathName -notmatch '--broker-safe\s*$') {
        throw "the upgraded broker created a non-broker-safe tunnel service"
    }
    Write-Host (Invoke-Native -FilePath $currentQuick -Arguments @(
        "desktop-client", "down", $tunnelName
    ))
    Wait-For -Description "the upgraded broker service teardown" `
        -Condition {
            $null -eq (Get-Service -Name $serviceName `
                -ErrorAction SilentlyContinue)
        }

    Invoke-Msi -Action uninstall -Path $currentInstallerPath `
        -LogPath $uninstallLog
    $currentInstalled = $false
    Wait-For -Description "the upgraded management service removal" `
        -Condition {
            $null -eq (Get-Service -Name $managerServiceName `
                -ErrorAction SilentlyContinue)
        }
    Write-Host (
        "v0.2.0 -> current MSI upgrade preserved the active tunnel, " +
        "installed the broker, and completed broker-safe restart/teardown"
    )
}
catch {
    Write-Warning "installed Windows desktop upgrade lifecycle failed: $_"
    foreach ($log in @($previousLog, $upgradeLog, $uninstallLog)) {
        if (Test-Path -LiteralPath $log -PathType Leaf) {
            Write-Host "last lines from $log"
            Get-Content -LiteralPath $log -Tail 120 | Write-Host
        }
    }
    throw
}
finally {
    foreach ($candidateQuick in @($currentQuick, $oldStagedQuick)) {
        if ([string]::IsNullOrWhiteSpace($candidateQuick) -or
            -not (Test-Path -LiteralPath $candidateQuick -PathType Leaf)) {
            continue
        }
        if (Get-Service -Name $serviceName -ErrorAction SilentlyContinue) {
            try {
                & $candidateQuick down $tunnelName 2>&1 | Out-Null
            }
            catch {
                Write-Warning "failed to stop upgrade fixture tunnel: $_"
            }
        }
    }
    if ($currentInstalled) {
        try {
            Invoke-Msi -Action uninstall -Path $currentInstallerPath `
                -LogPath $uninstallLog
        }
        catch {
            Write-Warning "failed to uninstall current MSI fixture: $_"
        }
    }
    elseif ($previousInstalled) {
        try {
            Invoke-Msi -Action uninstall -Path $previousInstallerPath `
                -LogPath $uninstallLog
        }
        catch {
            Write-Warning "failed to uninstall previous MSI fixture: $_"
        }
    }
    Remove-Item -LiteralPath $installedConfig -Force `
        -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $fixtureRoot -Recurse -Force `
        -ErrorAction SilentlyContinue
}
