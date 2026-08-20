param(
    [Parameter(Mandatory = $true)]
    [string] $Installer,
    [string] $InstallRoot = ""
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
. (Join-Path $PSScriptRoot "lifecycle-fixtures.ps1")

function Wait-For {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Description,
        [Parameter(Mandatory = $true)]
        [scriptblock] $Condition,
        [int] $TimeoutSeconds = 30
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
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "timed out waiting for ${Description}: $lastError"
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

function Wait-ProcessExit {
    param(
        [Parameter(Mandatory = $true)]
        [System.Diagnostics.Process] $Process,
        [Parameter(Mandatory = $true)]
        [string] $Description,
        [int] $TimeoutSeconds = 180
    )

    try {
        Wait-For -Description $Description -TimeoutSeconds $TimeoutSeconds `
            -Condition {
                $Process.Refresh()
                $Process.HasExited
            }
    }
    catch {
        try {
            $Process.Refresh()
            if (-not $Process.HasExited) {
                Stop-Process -Id $Process.Id -Force -ErrorAction Stop
            }
        }
        catch {
            Write-Warning "failed to terminate ${Description}: $_"
        }
        throw
    }
    $Process.Refresh()
    return $Process.ExitCode
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator
)) {
    throw "the installed desktop lifecycle requires an Administrator token"
}

$installerPath = (Resolve-Path -LiteralPath $Installer).Path
$defaultInstallRoot = Join-Path $env:ProgramFiles "wg-quic"
$installRoot = if ([string]::IsNullOrWhiteSpace($InstallRoot)) {
    $defaultInstallRoot
}
else {
    if (-not [IO.Path]::IsPathRooted($InstallRoot)) {
        throw "the requested install root must be absolute: $InstallRoot"
    }
    [IO.Path]::GetFullPath($InstallRoot)
}
$managerServiceName = "wg-quic-manager"
$manager = Join-Path $installRoot "wg-quic-manager.exe"
$fixtureRoot = Join-Path $env:TEMP "wg-quic-desktop-install-$PID"
$tunnelName = "wgqdesk$PID"
$sourceConfig = Join-Path $fixtureRoot "$tunnelName.conf"
$installedConfig = Join-Path (
    Join-Path $env:ProgramData "wg-quic\interfaces"
) "$tunnelName.conf"
$serviceName = "wg-quic-quick@$tunnelName"
$endpointPrefix = "192.0.2.200/32"
$ledgerPath = Join-Path $env:ProgramData "wg-quic\state\routes-v1.json"
$endpointRoutesBefore = @(
    Get-NetRoute -AddressFamily IPv4 -DestinationPrefix $endpointPrefix `
        -ErrorAction SilentlyContinue
)
$stdoutPath = Join-Path $fixtureRoot "desktop.stdout.log"
$stderrPath = Join-Path $fixtureRoot "desktop.stderr.log"
$resultPath = Join-Path $fixtureRoot "desktop.result.log"
$trayResultPath = Join-Path $fixtureRoot "desktop.tray.result.log"
$trayStdoutPath = Join-Path $fixtureRoot "desktop.tray.stdout.log"
$trayStderrPath = Join-Path $fixtureRoot "desktop.tray.stderr.log"
$msiLogPath = Join-Path $fixtureRoot "msiexec.log"
$desktop = $null
$desktopProcess = $null
$trayProcess = $null
$secondDesktopProcess = $null
$desktopIpcProbeProcess = $null
$filteredProcess = $null
$quick = $null
$core = $null
$installedWintun = $null
$stagedQuick = $null
$stagedCore = $null
$directRuntimeDirectory = $null
$installed = $false
$standardUserName = "wgqdesk$PID"
$standardUserCreated = $false
$filteredAdminUserName = "wgqadmin$PID"
$filteredAdminUserCreated = $false
$standardUserRoot = Join-Path $env:PUBLIC "wg-quic-ci-$PID"
$filteredRetiredRuntimeResult = Join-Path $standardUserRoot (
    "filtered-retired-runtime.txt"
)
$desktopIpcProbe = Join-Path $standardUserRoot "desktop-ipc-probe.exe"
$managementPipeSquatProbe = Join-Path $standardUserRoot (
    "management-pipe-squat-probe.exe"
)
$desktopIpcReady = Join-Path $standardUserRoot "desktop-ipc.ready.json"
$desktopIpcResult = Join-Path $standardUserRoot "desktop-ipc.result.txt"
$desktopIpcStdout = Join-Path $standardUserRoot "desktop-ipc.stdout.txt"
$desktopIpcStderr = Join-Path $standardUserRoot "desktop-ipc.stderr.txt"
$brokerStatusResult = Join-Path $standardUserRoot "broker-status.json"
$filteredAdminScript = Join-Path $standardUserRoot "filtered-admin-check.ps1"
$filteredAdminResult = Join-Path $standardUserRoot "filtered-admin-result.txt"

try {
    New-Item -ItemType Directory -Force -Path $fixtureRoot | Out-Null
    Write-Host "installing $installerPath"
    $install = Start-Process -FilePath "msiexec.exe" `
        -ArgumentList @(
            "/i",
            "`"$installerPath`"",
            "INSTALLDIR=`"$installRoot`"",
            "/qn",
            "/norestart",
            "/l*v",
            "`"$msiLogPath`""
        ) -PassThru
    $installExitCode = Wait-ProcessExit -Process $install `
        -Description "the MSI installation"
    if ($installExitCode -notin @(0, 3010)) {
        throw "MSI installer exited with code $installExitCode"
    }
    $installed = $true
    Write-Host "MSI installation completed"

    Wait-For -Description "the installed MSI application" -Condition {
        $quickItem = Get-ChildItem -LiteralPath $installRoot `
            -Recurse -File -Filter "wg-quic-quick.exe" `
            -ErrorAction SilentlyContinue |
            Where-Object {
                $_.DirectoryName.EndsWith(
                    "\bin",
                    [StringComparison]::OrdinalIgnoreCase
                )
            } |
            Select-Object -First 1
        $desktopItem = Get-ChildItem -LiteralPath $installRoot `
            -Recurse -File -Filter "wg-quic-desktop.exe" `
            -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if ($null -eq $quickItem -or $null -eq $desktopItem -or
            -not (Test-Path -LiteralPath $manager -PathType Leaf)) {
            return $false
        }
        $script:quick = $quickItem.FullName
        $script:desktop = $desktopItem.FullName
        (
            (Test-Path -LiteralPath $script:desktop -PathType Leaf) -and
            (Test-Path -LiteralPath $script:quick -PathType Leaf)
        )
    }
    Write-Host "desktop=$desktop"
    Write-Host "quick=$quick"
    Write-Host "manager=$manager"

    Wait-For -Description "the installed management service" `
        -TimeoutSeconds 60 -Condition {
            $candidateManagerService = Get-CimInstance `
                -ClassName Win32_Service `
                -Filter "Name='$managerServiceName'" `
                -ErrorAction SilentlyContinue
            $null -ne $candidateManagerService -and
                $candidateManagerService.State -eq "Running"
        }
    $managerService = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$managerServiceName'" -ErrorAction Stop
    if ($managerService.StartMode -ne "Auto") {
        throw (
            "$managerServiceName start mode is " +
            "$($managerService.StartMode), expected Auto"
        )
    }
    if ($managerService.StartName -ne "LocalSystem") {
        throw (
            "$managerServiceName runs as $($managerService.StartName), " +
            "expected LocalSystem"
        )
    }
    $expectedManagerPathName = "`"$manager`" broker-service"
    if (-not ($managerService.PathName.Equals(
        $expectedManagerPathName,
        [StringComparison]::OrdinalIgnoreCase
    ))) {
        throw (
            "$managerServiceName PathName is '$($managerService.PathName)', " +
            "expected '$expectedManagerPathName'"
        )
    }
    Write-Host (
        "installed management service is Running/Auto/LocalSystem at " +
        $expectedManagerPathName
    )

    $unsafeSids = @("S-1-5-32-545", "S-1-5-11", "S-1-1-0")
    $installedBin = Split-Path -Parent $quick
    foreach ($protectedInstallObject in @(
        $installRoot,
        $installedBin,
        $desktop,
        $quick,
        $manager
    )) {
        $installObjectAcl = Get-Acl -LiteralPath $protectedInstallObject
        $unsafeRule = @(
            $installObjectAcl.GetAccessRules(
                $true,
                $true,
                [Security.Principal.SecurityIdentifier]
            ) |
            Where-Object {
                $_.AccessControlType -eq "Allow" -and
                $_.IdentityReference.Value -in $unsafeSids -and
                (
                    $_.FileSystemRights.ToString() -match (
                        "Write|Modify|FullControl|Delete|TakeOwnership|" +
                        "ChangePermissions"
                    )
                )
            }
        )
        if ($unsafeRule.Count -ne 0) {
            throw "$protectedInstallObject is writable by unelevated users"
        }
    }
    Write-Host "installed directory and executable ACLs are protected"

    $administratorBrokerStatus = $null
    Wait-For -Description "the Administrator management broker status" `
        -TimeoutSeconds 30 -Condition {
            $statusText = Invoke-Native -FilePath $quick `
                -Arguments @("desktop-broker-status")
            $candidateStatus = $statusText | ConvertFrom-Json
            if ($candidateStatus.status -ne "ready") {
                throw "management broker returned $statusText"
            }
            $script:administratorBrokerStatus = $candidateStatus
            return $true
        }
    if ($administratorBrokerStatus.service_name -ne $managerServiceName) {
        throw (
            "management broker reported service_name " +
            "'$($administratorBrokerStatus.service_name)'"
        )
    }
    if ([int] $administratorBrokerStatus.protocol_version -ne 1) {
        throw (
            "management broker reported protocol_version " +
            "'$($administratorBrokerStatus.protocol_version)'"
        )
    }
    Write-Host "Administrator management broker status is ready"

    $core = Join-Path (Split-Path -Parent $quick) "wg-quic.exe"
    $installedWintun = Join-Path (Split-Path -Parent $quick) "wintun.dll"
    $privateKey = Invoke-Native -FilePath $core -Arguments @("genkey")
    $peerPrivateKey = Invoke-Native -FilePath $core -Arguments @("genkey")
    $peerPublicKey = $peerPrivateKey |
        & $core pubkey 2>&1 |
        Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "derive installed desktop peer public key failed"
    }
    $peerPublicKey = $peerPublicKey.Trim()
    $listenPort = Get-AvailableLifecycleUdpPort
    $endpointPort = 63000 + ($PID % 1000)
    @"
[Interface]
PrivateKey = $privateKey
Address = 198.19.0.1/32
ListenPort = $listenPort
MTU = 1380

[Peer]
PublicKey = $peerPublicKey
AllowedIPs = 198.19.0.2/32
Endpoint = 192.0.2.200:$endpointPort
PersistentKeepalive = 1
"@ | Set-Content -LiteralPath $sourceConfig -Encoding ascii

    $env:WG_QUIC_DESKTOP_INTEGRATION_SMOKE = "1"
    $env:WG_QUIC_DESKTOP_SMOKE_CONFIG = $sourceConfig
    $env:WG_QUIC_DESKTOP_SMOKE_NAME = $tunnelName
    $env:WG_QUIC_DESKTOP_SMOKE_RESULT = $resultPath
    # Poison the legacy helper request variables so this lifecycle proves the
    # installed desktop uses the authenticated broker request path instead.
    $env:WG_QUIC_DESKTOP_ACTION = "invalid-inherited-action"
    $env:WG_QUIC_DESKTOP_NAME = "invalid-inherited-name"
    $env:WG_QUIC_DESKTOP_SOURCE = "invalid-inherited-source"
    $env:WG_QUIC_DESKTOP_OVERWRITE = "invalid-inherited-overwrite"
    $desktopProcess = Start-Process -FilePath $desktop `
        -PassThru `
        -RedirectStandardOutput $stdoutPath `
        -RedirectStandardError $stderrPath
    Wait-For -Description "the installed desktop lifecycle" `
        -TimeoutSeconds 180 -Condition {
            $desktopProcess.Refresh()
            $desktopProcess.HasExited
        }
    $desktopOutput = @(
        if (Test-Path -LiteralPath $stdoutPath) {
            Get-Content -LiteralPath $stdoutPath -Raw
        }
        if (Test-Path -LiteralPath $stderrPath) {
            Get-Content -LiteralPath $stderrPath -Raw
        }
    ) -join "`n"
    if ($desktopProcess.ExitCode -ne 0) {
        throw (
            "installed desktop exited with code " +
            "$($desktopProcess.ExitCode)`n$desktopOutput"
        )
    }
    $desktopResult = if (Test-Path -LiteralPath $resultPath) {
        Get-Content -LiteralPath $resultPath -Raw
    }
    else {
        ""
    }
    if ($desktopResult -notmatch
        "installed desktop import/broker/service/status lifecycle passed") {
        throw (
            "installed desktop did not report lifecycle success`n" +
            "result=$desktopResult`n$desktopOutput"
        )
    }

    if (Get-Service -Name $serviceName -ErrorAction SilentlyContinue) {
        throw "installed desktop left service $serviceName behind"
    }
    if (Get-NetAdapter -Name $tunnelName -ErrorAction SilentlyContinue) {
        throw "installed desktop left Wintun adapter $tunnelName behind"
    }
    if (-not (Test-Path -LiteralPath $installedConfig -PathType Leaf)) {
        throw "installed desktop did not import $installedConfig"
    }
    $configAcl = Get-Acl -LiteralPath $installedConfig
    if (-not $configAcl.AreAccessRulesProtected) {
        throw "installed desktop configuration ACL inherits unexpected access"
    }
    $unsafeConfigRules = @(
        $configAcl.GetAccessRules(
            $true,
            $true,
            [Security.Principal.SecurityIdentifier]
        ) |
        Where-Object {
            $_.AccessControlType -eq "Allow" -and
            $_.IdentityReference.Value -in $unsafeSids
        }
    )
    if ($unsafeConfigRules.Count -ne 0) {
        throw "installed desktop configuration is accessible to ordinary users"
    }
    $configDirectoryAcl = Get-Acl -LiteralPath (Split-Path -Parent $installedConfig)
    $unsafeDirectoryWrite = @(
        $configDirectoryAcl.GetAccessRules(
            $true,
            $true,
            [Security.Principal.SecurityIdentifier]
        ) |
        Where-Object {
            $_.AccessControlType -eq "Allow" -and
            $_.IdentityReference.Value -in $unsafeSids -and
            $_.FileSystemRights.ToString() -match (
                "Write|Modify|FullControl|Delete|TakeOwnership|" +
                "ChangePermissions"
            )
        }
    )
    if ($unsafeDirectoryWrite.Count -ne 0) {
        throw "desktop configuration directory is writable by ordinary users"
    }
    $runtimeRoot = Join-Path $env:ProgramData "wg-quic\runtime"
    Wait-For -Description "the desktop smoke runtime cleanup" `
        -TimeoutSeconds 30 -Condition {
            @(
                Get-ChildItem -LiteralPath $runtimeRoot -Directory `
                    -Filter "run-*" -ErrorAction Stop
            ).Count -eq 0
        }

    Write-Host $desktopOutput

    Remove-Item Env:WG_QUIC_DESKTOP_INTEGRATION_SMOKE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SMOKE_CONFIG `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SMOKE_NAME `
        -ErrorAction SilentlyContinue
    $env:WG_QUIC_DESKTOP_TRAY_SMOKE = "1"
    $env:WG_QUIC_DESKTOP_SMOKE_RESULT = $trayResultPath
    $trayProcess = Start-Process -FilePath $desktop -PassThru `
        -RedirectStandardOutput $trayStdoutPath `
        -RedirectStandardError $trayStderrPath
    Wait-For -Description "the installed desktop tray" -TimeoutSeconds 30 `
        -Condition {
            (Test-Path -LiteralPath $trayResultPath -PathType Leaf) -and
            (Get-Content -LiteralPath $trayResultPath -Raw) -match
                "desktop tray smoke ready"
        }
    $trayProcess.Refresh()
    if (-not $trayProcess.CloseMainWindow()) {
        throw "installed desktop did not expose a closable main window"
    }
    Wait-For -Description "the desktop close-to-tray handler" `
        -TimeoutSeconds 30 -Condition {
            (Get-Content -LiteralPath $trayResultPath -Raw) -match
                "desktop tray hidden"
        }
    $trayProcess.Refresh()
    if ($trayProcess.HasExited) {
        throw "closing the main window terminated the tray process"
    }
    $secondDesktopProcess = Start-Process -FilePath $desktop -PassThru
    $secondExitCode = Wait-ProcessExit -Process $secondDesktopProcess `
        -Description "the second desktop instance" -TimeoutSeconds 30
    if ($secondExitCode -ne 0) {
        throw "second desktop instance exited with code $secondExitCode"
    }
    Wait-For -Description "the single-instance activation callback" `
        -TimeoutSeconds 30 -Condition {
            (Get-Content -LiteralPath $trayResultPath -Raw) -match
                "desktop tray single-instance activated"
        }
    $trayProcess.Refresh()
    if ($trayProcess.HasExited) {
        throw "the second desktop instance replaced the tray owner"
    }
    Stop-Process -Id $trayProcess.Id -Force -ErrorAction Stop
    $null = Wait-ProcessExit -Process $trayProcess `
        -Description "the tray smoke process exit" -TimeoutSeconds 30
    Remove-Item Env:WG_QUIC_DESKTOP_TRAY_SMOKE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_ACTION `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_NAME `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SOURCE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_OVERWRITE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SMOKE_RESULT `
        -ErrorAction SilentlyContinue
    Write-Host "installed Windows tray and single-instance lifecycle passed"

    Write-Host (Invoke-Native -FilePath $quick -Arguments @("up", $tunnelName))
    Wait-For -Description "the active installed desktop service" -Condition {
        (Get-Service -Name $serviceName -ErrorAction SilentlyContinue).Status `
            -eq "Running"
    }
    Wait-For -Description "the active installed Wintun adapter" -Condition {
        $null -ne (Get-NetAdapter -Name $tunnelName `
            -ErrorAction SilentlyContinue)
    }
    Wait-For -Description "the active installed endpoint route lease" `
        -Condition {
            if (-not (Test-Path -LiteralPath $ledgerPath -PathType Leaf)) {
                return $false
            }
            $candidateLedger = Get-Content -LiteralPath $ledgerPath -Raw |
                ConvertFrom-Json
            @(
                $candidateLedger.routes |
                    ForEach-Object { $_.owners } |
                    Where-Object { $_.tunnel -eq $tunnelName }
            ).Count -gt 0
        }
    $serviceInfo = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$serviceName'" -ErrorAction Stop
    $stagedQuick = if ($serviceInfo.PathName -match '^"([^"]+)"') {
        $Matches[1]
    }
    elseif ($serviceInfo.PathName -match '^(\S+)') {
        $Matches[1]
    }
    else {
        throw "cannot parse staged service path $($serviceInfo.PathName)"
    }
    $stagedCore = Join-Path (Split-Path -Parent $stagedQuick) "wg-quic.exe"
    if (-not (Test-Path -LiteralPath $stagedCore -PathType Leaf)) {
        throw "active service runtime is missing $stagedCore"
    }
    $directRuntimeDirectory = Split-Path -Parent $stagedQuick

    $passwordText = "Wgq!1aA$([Guid]::NewGuid().ToString('N'))"
    $securePassword = ConvertTo-SecureString $passwordText `
        -AsPlainText -Force
    $standardUser = New-LocalUser -Name $standardUserName `
        -Password $securePassword -PasswordNeverExpires `
        -UserMayNotChangePassword -AccountNeverExpires
    $standardUserCreated = $true
    $usersGroupSid = [Security.Principal.SecurityIdentifier]::new(
        "S-1-5-32-545"
    )
    $usersGroupMembers = @(
        Get-LocalGroupMember -SID $usersGroupSid -ErrorAction Stop
    )
    if ($standardUser.SID.Value -notin @($usersGroupMembers.SID.Value)) {
        Add-LocalGroupMember -SID $usersGroupSid `
            -Member $standardUser -ErrorAction Stop
    }
    New-Item -ItemType Directory -Force -Path $standardUserRoot |
        Out-Null
    & icacls.exe $standardUserRoot /grant:r (
        "*$($standardUser.SID.Value):(OI)(CI)M"
    ) | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "grant standard-user fixture access failed"
    }
    $repositoryRoot = (Resolve-Path -LiteralPath (
        Join-Path $PSScriptRoot "..\.."
    )).Path
    $goExecutable = (Get-Command "go.exe" -ErrorAction Stop).Source
    Push-Location $repositoryRoot
    try {
        Invoke-Native -FilePath $goExecutable -Arguments @(
            "build",
            "-trimpath",
            "-o", $desktopIpcProbe,
            "./tests/windows/desktop-ipc-probe"
        ) | Out-Null
        Invoke-Native -FilePath $goExecutable -Arguments @(
            "build",
            "-trimpath",
            "-o", $managementPipeSquatProbe,
            "./tests/windows/management-pipe-squat-probe"
        ) | Out-Null
    }
    finally {
        Pop-Location
    }
    if (-not (Test-Path -LiteralPath $desktopIpcProbe -PathType Leaf)) {
        throw "desktop IPC probe build did not produce $desktopIpcProbe"
    }
    if (-not (Test-Path -LiteralPath $managementPipeSquatProbe -PathType Leaf)) {
        throw (
            "management pipe squat probe build did not produce " +
            $managementPipeSquatProbe
        )
    }
    $positiveSquatOutput = @(& $managementPipeSquatProbe created 2>&1)
    if ($LASTEXITCODE -ne 0 -or
        ($positiveSquatOutput | Out-String) -notmatch
            "management pipe instance creation succeeded") {
        throw (
            "elevated management pipe instance positive control failed`: " +
            ($positiveSquatOutput | Out-String)
        )
    }
    Write-Host (
        "elevated management pipe instance positive control passed with " +
        "the broker's exact pipe parameters"
    )
    $standardCredential = [Management.Automation.PSCredential]::new(
        "$env:COMPUTERNAME\$standardUserName",
        $securePassword
    )
    $desktopIpcProbeProcess = Start-Process -FilePath $desktopIpcProbe `
        -ArgumentList @(
            "-name", $tunnelName,
            "-ready", "`"$desktopIpcReady`"",
            "-result", "`"$desktopIpcResult`""
        ) -Credential $standardCredential -LoadUserProfile -PassThru `
        -WorkingDirectory $standardUserRoot `
        -RedirectStandardOutput $desktopIpcStdout `
        -RedirectStandardError $desktopIpcStderr
    $desktopIpcPipe = $null
    Wait-For -Description "the standard-user desktop IPC listener" `
        -TimeoutSeconds 30 -Condition {
            if (-not (Test-Path -LiteralPath $desktopIpcReady -PathType Leaf)) {
                return $false
            }
            $ready = Get-Content -LiteralPath $desktopIpcReady -Raw |
                ConvertFrom-Json
            if ($ready.pid -ne $desktopIpcProbeProcess.Id) {
                throw (
                    "desktop IPC ready PID $($ready.pid) does not match " +
                    "probe PID $($desktopIpcProbeProcess.Id)"
                )
            }
            if ($ready.elevated -ne $false) {
                throw "desktop IPC probe unexpectedly reported an elevated token"
            }
            if ($ready.pipe -notmatch (
                '^\\\\\.\\pipe\\wg-quic-desktop-\d+-[0-9a-f]{32}$'
            )) {
                throw "desktop IPC probe returned invalid pipe $($ready.pipe)"
            }
            $script:desktopIpcPipe = [string] $ready.pipe
            return $true
        }

    # Directly run the installed production helper with the runner's existing
    # Administrator token. The listener belongs to a different standard-user
    # token, so this covers the actual low-token -> high-token pipe boundary
    # without trying to automate the secure UAC desktop.
    $desktopEnvironmentNames = @(
        "WG_QUIC_DESKTOP_PIPE",
        "WG_QUIC_DESKTOP_ACTION",
        "WG_QUIC_DESKTOP_NAME",
        "WG_QUIC_DESKTOP_SOURCE",
        "WG_QUIC_DESKTOP_OVERWRITE",
        "WG_QUIC_DESKTOP_INTEGRATION_SMOKE",
        "WG_QUIC_DESKTOP_SMOKE_CONFIG",
        "WG_QUIC_DESKTOP_SMOKE_NAME",
        "WG_QUIC_DESKTOP_SMOKE_RESULT",
        "WG_QUIC_DESKTOP_TRAY_SMOKE",
        "WG_QUIC_ELEVATED_EXE"
    )
    foreach ($environmentName in $desktopEnvironmentNames) {
        Remove-Item -LiteralPath "Env:$environmentName" `
            -ErrorAction SilentlyContinue
    }
    $env:WG_QUIC_DESKTOP_PIPE = "invalid-inherited-pipe"
    $env:WG_QUIC_DESKTOP_ACTION = "invalid-inherited-action"
    $env:WG_QUIC_DESKTOP_NAME = "invalid-inherited-name"
    $env:WG_QUIC_DESKTOP_SOURCE = "invalid-inherited-source"
    $env:WG_QUIC_DESKTOP_OVERWRITE = "invalid-inherited-overwrite"
    $env:WG_QUIC_ELEVATED_EXE = "invalid-inherited-executable"
    try {
        Invoke-Native -FilePath $quick -Arguments @(
            "desktop-helper", $desktopIpcPipe
        ) | Out-Null
    }
    finally {
        foreach ($environmentName in $desktopEnvironmentNames) {
            Remove-Item -LiteralPath "Env:$environmentName" `
                -ErrorAction SilentlyContinue
        }
    }
    $desktopIpcExitCode = Wait-ProcessExit `
        -Process $desktopIpcProbeProcess `
        -Description "the standard-user desktop IPC probe" `
        -TimeoutSeconds 30
    $desktopIpcResultText = if (
        Test-Path -LiteralPath $desktopIpcResult -PathType Leaf
    ) {
        (Get-Content -LiteralPath $desktopIpcResult -Raw).Trim()
    }
    else {
        "missing result"
    }
    if ($desktopIpcExitCode -ne 0 -or $desktopIpcResultText -ne "passed") {
        $desktopIpcOutput = @(
            $desktopIpcResultText
            if (Test-Path -LiteralPath $desktopIpcStdout) {
                Get-Content -LiteralPath $desktopIpcStdout -Raw
            }
            if (Test-Path -LiteralPath $desktopIpcStderr) {
                Get-Content -LiteralPath $desktopIpcStderr -Raw
            }
        ) -join "`n"
        throw "standard-user desktop IPC probe failed`n$desktopIpcOutput"
    }
    Write-Host (
        "installed Windows cross-token desktop helper IPC passed " +
        "(standard-user listener -> Administrator helper)"
    )

    $brokerConfigPathsBefore = @(
        Get-ChildItem -LiteralPath (Split-Path -Parent $installedConfig) `
            -Filter "*.conf" -File -ErrorAction Stop |
            ForEach-Object { $_.FullName } |
            Sort-Object
    )
    $brokerConfigHashBefore = (
        Get-FileHash -LiteralPath $installedConfig -Algorithm SHA256
    ).Hash
    $brokerTunnelServicesBefore = @(
        Get-Service -ErrorAction Stop |
            Where-Object { $_.Name.StartsWith("wg-quic-quick@") } |
            ForEach-Object { "$($_.Name)|$($_.Status)" } |
            Sort-Object
    )
    $brokerAdapterBefore = Get-NetAdapter -Name $tunnelName `
        -ErrorAction Stop
    $brokerAdapterIdentityBefore = (
        "$($brokerAdapterBefore.Name)|$($brokerAdapterBefore.ifIndex)|" +
        "$($brokerAdapterBefore.Status)|" +
        $brokerAdapterBefore.InterfaceDescription
    )

    $standardScript = Join-Path $standardUserRoot "status-check.ps1"
    $standardResult = Join-Path $standardUserRoot "result.txt"
    $standardStdout = Join-Path $standardUserRoot "stdout.txt"
    $standardStderr = Join-Path $standardUserRoot "stderr.txt"
    @'
param(
    [string] $Core,
    [string] $Tunnel,
    [string] $Result,
    [string] $BrokerResult,
    [string] $Quick,
    [string] $PipeSquatProbe,
    [string] $ConfigDirectory,
    [string] $ConfigPath
)
$ErrorActionPreference = "Stop"
try {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if ($principal.IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator
    )) {
        throw "status check unexpectedly has an Administrator token"
    }
    if (-not $principal.IsInRole(
        [Security.Principal.WindowsBuiltInRole]::User
    )) {
        throw "status check token is not a member of the built-in Users group"
    }
    $squatOutput = @(& $PipeSquatProbe denied 2>&1)
    if ($LASTEXITCODE -ne 0 -or
        ($squatOutput | Out-String) -notmatch
            "management pipe instance creation denied") {
        throw (
            "ordinary user management pipe squat probe failed`: " +
            ($squatOutput | Out-String)
        )
    }
    $brokerOutput = @(& $Quick desktop-broker-status 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw (
            "desktop-broker-status failed with exit code $LASTEXITCODE`: " +
            ($brokerOutput | Out-String)
        )
    }
    $brokerText = ($brokerOutput | Out-String).Trim()
    Set-Content -LiteralPath $BrokerResult -Value $brokerText `
        -Encoding ascii
    $brokerStatus = $brokerText | ConvertFrom-Json
    if ($brokerStatus.status -ne "unauthorized") {
        throw "ordinary user broker status is not unauthorized: $brokerText"
    }
    if ($brokerStatus.service_name -ne "wg-quic-manager") {
        throw "ordinary user broker returned wrong service: $brokerText"
    }
    if ([int] $brokerStatus.protocol_version -ne 1) {
        throw "ordinary user broker returned wrong protocol: $brokerText"
    }
    $profiles = @(Get-ChildItem -LiteralPath $ConfigDirectory -Filter "*.conf")
    if ($ConfigPath -notin @($profiles.FullName)) {
        throw "ordinary user cannot enumerate the installed profile"
    }
    $configReadable = $false
    try {
        Get-Content -LiteralPath $ConfigPath -Raw | Out-Null
        $configReadable = $true
    }
    catch [UnauthorizedAccessException] {
    }
    if ($configReadable) {
        throw "ordinary user can read the protected tunnel configuration"
    }
    $primary = [IO.Pipes.NamedPipeClientStream]::new(
        ".",
        "wg-quic-$Tunnel",
        [IO.Pipes.PipeDirection]::InOut
    )
    $denied = $false
    try {
        $primary.Connect(1500)
    }
    catch [UnauthorizedAccessException] {
        $denied = $true
    }
    finally {
        $primary.Dispose()
    }
    if (-not $denied) {
        throw "ordinary user reached the privileged control pipe"
    }
    $output = @(& $Core show --json 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw "unprivileged show failed: $($output | Out-String)"
    }
    $statuses = @(($output | Out-String) | ConvertFrom-Json)
    $status = @($statuses | Where-Object {
        $_.interface -eq $Tunnel -and $_.state -eq "up"
    })
    if ($status.Count -ne 1) {
        throw "unexpected unprivileged status: $($statuses | ConvertTo-Json)"
    }
    Set-Content -LiteralPath $Result -Value "passed" -Encoding ascii
}
catch {
    Set-Content -LiteralPath $Result -Value "failed: $_" -Encoding utf8
    throw
}
'@ | Set-Content -LiteralPath $standardScript -Encoding utf8
    $standardProcess = Start-Process -FilePath (
        Join-Path $env:SystemRoot "System32\WindowsPowerShell\v1.0\powershell.exe"
    ) -ArgumentList @(
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-ExecutionPolicy", "Bypass",
        "-File", "`"$standardScript`"",
        "-Core", "`"$core`"",
        "-Tunnel", $tunnelName,
        "-Result", "`"$standardResult`"",
        "-BrokerResult", "`"$brokerStatusResult`"",
        "-Quick", "`"$quick`"",
        "-PipeSquatProbe", "`"$managementPipeSquatProbe`"",
        "-ConfigDirectory", "`"$(Split-Path -Parent $installedConfig)`"",
        "-ConfigPath", "`"$installedConfig`""
    ) -Credential $standardCredential -LoadUserProfile -PassThru `
        -WorkingDirectory $standardUserRoot `
        -RedirectStandardOutput $standardStdout `
        -RedirectStandardError $standardStderr
    $standardExitCode = Wait-ProcessExit -Process $standardProcess `
        -Description "the standard-user status check" -TimeoutSeconds 45
    $standardResultText = if (Test-Path -LiteralPath $standardResult) {
        Get-Content -LiteralPath $standardResult -Raw
    }
    else {
        "missing result"
    }
    if ($standardExitCode -ne 0 -or $standardResultText -notmatch "passed") {
        $standardOutput = @(
            $standardResultText
            if (Test-Path -LiteralPath $standardStdout) {
                Get-Content -LiteralPath $standardStdout -Raw
            }
            if (Test-Path -LiteralPath $standardStderr) {
                Get-Content -LiteralPath $standardStderr -Raw
            }
        ) -join "`n"
        throw "standard-user status check failed`n$standardOutput"
    }
    $standardBrokerStatus = if (
        Test-Path -LiteralPath $brokerStatusResult -PathType Leaf
    ) {
        Get-Content -LiteralPath $brokerStatusResult -Raw |
            ConvertFrom-Json
    }
    else {
        throw "standard-user broker status result is missing"
    }
    if ($standardBrokerStatus.status -ne "unauthorized") {
        throw (
            "standard-user broker boundary returned " +
            "'$($standardBrokerStatus.status)'"
        )
    }
    $brokerConfigPathsAfter = @(
        Get-ChildItem -LiteralPath (Split-Path -Parent $installedConfig) `
            -Filter "*.conf" -File -ErrorAction Stop |
            ForEach-Object { $_.FullName } |
            Sort-Object
    )
    if (@(Compare-Object $brokerConfigPathsBefore $brokerConfigPathsAfter).Count `
        -ne 0) {
        throw "ordinary-user broker status changed the installed profiles"
    }
    $brokerConfigHashAfter = (
        Get-FileHash -LiteralPath $installedConfig -Algorithm SHA256
    ).Hash
    if ($brokerConfigHashAfter -ne $brokerConfigHashBefore) {
        throw "ordinary-user broker status changed the tunnel configuration"
    }
    $brokerTunnelServicesAfter = @(
        Get-Service -ErrorAction Stop |
            Where-Object { $_.Name.StartsWith("wg-quic-quick@") } |
            ForEach-Object { "$($_.Name)|$($_.Status)" } |
            Sort-Object
    )
    if (@(Compare-Object `
        $brokerTunnelServicesBefore $brokerTunnelServicesAfter
    ).Count -ne 0) {
        throw "ordinary-user broker status changed tunnel services"
    }
    if ((Get-Service -Name $managerServiceName -ErrorAction Stop).Status `
        -ne "Running") {
        throw "ordinary-user broker status stopped the management service"
    }
    $brokerAdapterAfter = Get-NetAdapter -Name $tunnelName `
        -ErrorAction Stop
    $brokerAdapterIdentityAfter = (
        "$($brokerAdapterAfter.Name)|$($brokerAdapterAfter.ifIndex)|" +
        "$($brokerAdapterAfter.Status)|" +
        $brokerAdapterAfter.InterfaceDescription
    )
    if ($brokerAdapterIdentityAfter -ne $brokerAdapterIdentityBefore) {
        throw "ordinary-user broker status changed the active adapter"
    }
    Write-Host "installed Windows standard-user status boundary passed"
    Write-Host (
        "ordinary-user management broker status is unauthorized with no " +
        "configuration, service, or adapter side effects"
    )

    $enableLUA = (
        Get-ItemProperty -LiteralPath (
            "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System"
        ) -Name EnableLUA -ErrorAction Stop
    ).EnableLUA
    if ([int] $enableLUA -ne 1) {
        throw "installed filtered-Administrator lifecycle requires EnableLUA=1"
    }
    Write-Host (Invoke-Native -FilePath $quick `
        -Arguments @("down", $tunnelName))
    Wait-For -Description "the direct service teardown before broker mutation" `
        -TimeoutSeconds 60 -Condition {
            $null -eq (Get-Service -Name $serviceName `
                -ErrorAction SilentlyContinue)
        }
    Wait-For -Description "the direct adapter teardown before broker mutation" `
        -TimeoutSeconds 60 -Condition {
            $null -eq (Get-NetAdapter -Name $tunnelName `
                -ErrorAction SilentlyContinue)
        }
    Wait-For -Description "the retired direct service runtime cleanup" `
        -TimeoutSeconds 30 -Condition {
            -not (Test-Path -LiteralPath $directRuntimeDirectory)
        }
    Write-Host (Invoke-Native -FilePath $quick `
        -Arguments @("desktop-client", "up", $tunnelName))
    Wait-For -Description "the broker-safe service before filtered mutation" `
        -TimeoutSeconds 60 -Condition {
            (Get-Service -Name $serviceName `
                -ErrorAction SilentlyContinue).Status -eq "Running"
        }
    Wait-For -Description "the broker-safe adapter before filtered mutation" `
        -TimeoutSeconds 60 -Condition {
            $null -ne (Get-NetAdapter -Name $tunnelName `
                -ErrorAction SilentlyContinue)
        }
    $brokerSafeService = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$serviceName'" -ErrorAction Stop
    if ($brokerSafeService.PathName -notmatch '--broker-safe\s*$') {
        throw (
            "management broker did not mark the staged service broker-safe`: " +
            $brokerSafeService.PathName
        )
    }
    $filteredAdminUser = New-LocalUser -Name $filteredAdminUserName `
        -Password $securePassword -PasswordNeverExpires `
        -UserMayNotChangePassword -AccountNeverExpires
    $filteredAdminUserCreated = $true
    $administratorsGroupSid = [Security.Principal.SecurityIdentifier]::new(
        "S-1-5-32-544"
    )
    Add-LocalGroupMember -SID $administratorsGroupSid `
        -Member $filteredAdminUser -ErrorAction Stop
    & icacls.exe $standardUserRoot /grant:r (
        "*$($filteredAdminUser.SID.Value):(OI)(CI)M"
    ) | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "grant filtered Administrator fixture access failed"
    }
    $filteredAdminScriptContent = @'
$Quick = __WG_QUIC_QUICK__
$Tunnel = __WG_QUIC_TUNNEL__
$TunnelService = __WG_QUIC_SERVICE__
$ConfigPath = __WG_QUIC_CONFIG_PATH__
$ExpectedListenPort = __WG_QUIC_LISTEN_PORT__
$ExpectedEndpointPort = __WG_QUIC_ENDPOINT_PORT__
$RetiredRuntimeResult = __WG_QUIC_RETIRED_RUNTIME_RESULT__
$Result = __WG_QUIC_RESULT__
$ErrorActionPreference = "Stop"
$LifecycleDeadline = [DateTime]::UtcNow.AddSeconds(360)
function Wait-ForFiltered {
    param(
        [string] $Description,
        [scriptblock] $Condition
    )
    do {
        if (& $Condition) {
            return
        }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $LifecycleDeadline)
    throw "timed out waiting for $Description"
}
try {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if ($principal.IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator
    )) {
        throw "filtered-admin check unexpectedly has an elevated token"
    }
    if (-not $principal.IsInRole(
        [Security.Principal.WindowsBuiltInRole]::User
    )) {
        throw "filtered-admin token is not in the built-in Users group"
    }
    $brokerOutput = @(& $Quick desktop-broker-status 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw (
            "desktop-broker-status failed with exit code $LASTEXITCODE`: " +
            ($brokerOutput | Out-String)
        )
    }
    $brokerText = ($brokerOutput | Out-String).Trim()
    $brokerStatus = $brokerText | ConvertFrom-Json
    if ($brokerStatus.status -ne "ready") {
        throw "filtered-admin broker status is not ready: $brokerText"
    }
    if ($brokerStatus.service_name -ne "wg-quic-manager" -or
        [int] $brokerStatus.protocol_version -ne 1) {
        throw "filtered-admin broker metadata is invalid: $brokerText"
    }
    $checkOutput = @(& $Quick desktop-client check $Tunnel 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw (
            "filtered-admin desktop check failed with exit code " +
            "$LASTEXITCODE`: $($checkOutput | Out-String)"
        )
    }
    if (($checkOutput | Out-String) -notmatch
        "configuration is valid for wg-quic-quick") {
        throw "filtered-admin desktop check returned unexpected output"
    }
    $directReadDenied = $false
    try {
        Get-Content -LiteralPath $ConfigPath -Raw -ErrorAction Stop |
            Out-Null
    }
    catch [UnauthorizedAccessException] {
        $directReadDenied = $true
    }
    if (-not $directReadDenied) {
        throw "filtered-admin token directly read the protected configuration"
    }
    $readOutput = @(& $Quick desktop-client read $Tunnel 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw (
            "filtered-admin desktop read failed with exit code " +
            "$LASTEXITCODE`: $($readOutput | Out-String)"
        )
    }
    $readText = $readOutput | Out-String
    if ($readText -notmatch '(?m)^\[Interface\]\s*$' -or
        $readText -notmatch '(?m)^PrivateKey\s*=' -or
        $readText -notmatch (
            "(?m)^ListenPort\s*=\s*${ExpectedListenPort}\s*$"
        ) -or
        $readText -notmatch (
            "(?m)^Endpoint\s*=\s*192\.0\.2\.200:" +
            "${ExpectedEndpointPort}\s*$"
        )) {
        throw "filtered-admin desktop read returned unexpected configuration"
    }
    $beforeDownService = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$TunnelService'" -ErrorAction Stop
    $beforeDownQuick = if ($beforeDownService.PathName -match '^"([^"]+)"') {
        $Matches[1]
    }
    elseif ($beforeDownService.PathName -match '^(\S+)') {
        $Matches[1]
    }
    else {
        throw "cannot parse pre-down runtime $($beforeDownService.PathName)"
    }
    $beforeDownRuntime = Split-Path -Parent $beforeDownQuick
    Set-Content -LiteralPath $RetiredRuntimeResult `
        -Value $beforeDownRuntime -Encoding utf8
    $downOutput = @(& $Quick desktop-client down $Tunnel 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw (
            "filtered-admin desktop down failed with exit code " +
            "$LASTEXITCODE`: $($downOutput | Out-String)"
        )
    }
    Wait-ForFiltered -Description "filtered-admin service teardown" `
        -Condition {
            $null -eq (Get-Service -Name $TunnelService `
                -ErrorAction SilentlyContinue)
        }
    Wait-ForFiltered -Description "filtered-admin adapter teardown" `
        -Condition {
            $null -eq (Get-NetAdapter -Name $Tunnel `
                -ErrorAction SilentlyContinue)
        }
    $upOutput = @(& $Quick desktop-client up $Tunnel 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw (
            "filtered-admin desktop up failed with exit code " +
            "$LASTEXITCODE`: $($upOutput | Out-String)"
        )
    }
    Wait-ForFiltered -Description "filtered-admin service startup" `
        -Condition {
            (Get-Service -Name $TunnelService `
                -ErrorAction SilentlyContinue).Status -eq "Running"
        }
    Wait-ForFiltered -Description "filtered-admin adapter startup" `
        -Condition {
            $null -ne (Get-NetAdapter -Name $Tunnel `
                -ErrorAction SilentlyContinue)
        }
    $afterUpService = Get-CimInstance -ClassName Win32_Service `
        -Filter "Name='$TunnelService'" -ErrorAction Stop
    $afterUpQuick = if ($afterUpService.PathName -match '^"([^"]+)"') {
        $Matches[1]
    }
    elseif ($afterUpService.PathName -match '^(\S+)') {
        $Matches[1]
    }
    else {
        throw "cannot parse post-up runtime $($afterUpService.PathName)"
    }
    if ((Split-Path -Parent $afterUpQuick).Equals(
        $beforeDownRuntime,
        [StringComparison]::OrdinalIgnoreCase
    )) {
        throw "filtered-admin up reused its retired random runtime"
    }
    if ((Get-Service -Name "wg-quic-manager" -ErrorAction Stop).Status `
        -ne "Running") {
        throw "filtered-admin mutation stopped the management service"
    }
    Set-Content -LiteralPath $Result -Value "passed" -Encoding ascii
}
catch {
    Set-Content -LiteralPath $Result -Value "failed: $_" -Encoding utf8
    throw
}
'@
    $filteredAdminScriptContent = $filteredAdminScriptContent.Replace(
        "__WG_QUIC_QUICK__",
        "'" + $quick.Replace("'", "''") + "'"
    )
    $filteredAdminScriptContent = $filteredAdminScriptContent.Replace(
        "__WG_QUIC_TUNNEL__",
        "'" + $tunnelName.Replace("'", "''") + "'"
    )
    $filteredAdminScriptContent = $filteredAdminScriptContent.Replace(
        "__WG_QUIC_SERVICE__",
        "'" + $serviceName.Replace("'", "''") + "'"
    )
    $filteredAdminScriptContent = $filteredAdminScriptContent.Replace(
        "__WG_QUIC_CONFIG_PATH__",
        "'" + $installedConfig.Replace("'", "''") + "'"
    )
    $filteredAdminScriptContent = $filteredAdminScriptContent.Replace(
        "__WG_QUIC_LISTEN_PORT__",
        [string] $listenPort
    )
    $filteredAdminScriptContent = $filteredAdminScriptContent.Replace(
        "__WG_QUIC_ENDPOINT_PORT__",
        [string] $endpointPort
    )
    $filteredAdminScriptContent = $filteredAdminScriptContent.Replace(
        "__WG_QUIC_RETIRED_RUNTIME_RESULT__",
        "'" + $filteredRetiredRuntimeResult.Replace("'", "''") + "'"
    )
    $filteredAdminScriptContent = $filteredAdminScriptContent.Replace(
        "__WG_QUIC_RESULT__",
        "'" + $filteredAdminResult.Replace("'", "''") + "'"
    )
    $filteredAdminScriptContent | Set-Content `
        -LiteralPath $filteredAdminScript -Encoding utf8
    $taskPowerShell = Join-Path $env:SystemRoot (
        "System32\WindowsPowerShell\v1.0\powershell.exe"
    )
    $limitedTaskExitCode = 0
    # Task Scheduler password logons can return the full Administrator token
    # even with /RL LIMITED. CreateProcessWithLogonW, used by Start-Process
    # -Credential, performs a normal interactive logon and gives this asInvoker
    # process the real UAC-filtered linked token.
    $filteredCredential = [Management.Automation.PSCredential]::new(
        "$env:COMPUTERNAME\$filteredAdminUserName",
        $securePassword
    )
    $filteredProcess = Start-Process -FilePath $taskPowerShell `
        -ArgumentList @(
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-ExecutionPolicy", "Bypass",
            "-File", "`"$filteredAdminScript`""
        ) `
        -Credential $filteredCredential `
        -LoadUserProfile `
        -WorkingDirectory $standardUserRoot `
        -PassThru
    $limitedTaskExitCode = Wait-ProcessExit `
        -Process $filteredProcess `
        -Description "the filtered-admin broker lifecycle" `
        -TimeoutSeconds 390
    $filteredAdminResultText = (
        Get-Content -LiteralPath $filteredAdminResult -Raw
    ).Trim()
    if ($limitedTaskExitCode -ne 0 -or
        $filteredAdminResultText -ne "passed") {
        throw (
            "filtered-admin broker lifecycle failed with task result " +
            "${limitedTaskExitCode}: " +
            $filteredAdminResultText
        )
    }

    if (-not (Test-Path -LiteralPath $filteredRetiredRuntimeResult `
        -PathType Leaf)) {
        throw "filtered-admin did not report its retired runtime"
    }
    $filteredRetiredRuntime = (
        Get-Content -LiteralPath $filteredRetiredRuntimeResult -Raw
    ).Trim()
    Wait-For -Description "the filtered-admin retired runtime cleanup" `
        -TimeoutSeconds 60 -Condition {
            -not (Test-Path -LiteralPath $filteredRetiredRuntime)
        }
    if ((Get-Service -Name $serviceName -ErrorAction Stop).Status `
        -ne "Running") {
        throw "filtered-admin mutation did not restore the tunnel service"
    }
    if ($null -eq (Get-NetAdapter -Name $tunnelName `
        -ErrorAction SilentlyContinue)) {
        throw "filtered-admin mutation did not restore the Wintun adapter"
    }
    $restoredBrokerSafeService = Get-CimInstance `
        -ClassName Win32_Service -Filter "Name='$serviceName'" `
        -ErrorAction Stop
    if ($restoredBrokerSafeService.PathName -notmatch '--broker-safe\s*$') {
        throw (
            "filtered-admin up restored a non-broker-safe service`: " +
            $restoredBrokerSafeService.PathName
        )
    }
    $stagedQuick = if ($restoredBrokerSafeService.PathName -match `
        '^"([^"]+)"') {
        $Matches[1]
    }
    elseif ($restoredBrokerSafeService.PathName -match '^(\S+)') {
        $Matches[1]
    }
    else {
        throw (
            "cannot parse restored service runtime " +
            $restoredBrokerSafeService.PathName
        )
    }
    $activeRuntimeDirectory = Split-Path -Parent $stagedQuick
    $stagedCore = Join-Path $activeRuntimeDirectory "wg-quic.exe"
    if (-not (Test-Path -LiteralPath $stagedCore -PathType Leaf)) {
        throw "restored active service runtime is missing $stagedCore"
    }
    $remainingRuntimeDirectories = @(
        Get-ChildItem -LiteralPath $runtimeRoot -Directory `
            -Filter "run-*" -ErrorAction Stop
    )
    if ($remainingRuntimeDirectories.Count -ne 1 -or
        -not $remainingRuntimeDirectories[0].FullName.Equals(
            $activeRuntimeDirectory,
            [StringComparison]::OrdinalIgnoreCase
        )) {
        throw (
            "runtime cleanup left unexpected directories: " +
            ($remainingRuntimeDirectories.FullName -join ", ")
        )
    }
    Write-Host (
        "a real UAC-filtered Administrator logon used the management broker " +
        "CLI without a prompt; the installed asInvoker desktop independently " +
        "completed import/check/up/down, and the CLI restored the primary " +
        "service and adapter"
    )

    $uninstall = Start-Process -FilePath "msiexec.exe" `
        -ArgumentList @(
            "/x",
            "`"$installerPath`"",
            "/qn",
            "/norestart"
        ) -PassThru
    $uninstallExitCode = Wait-ProcessExit -Process $uninstall `
        -Description "the active-tunnel MSI uninstall"
    if ($uninstallExitCode -notin @(0, 3010)) {
        throw "MSI uninstall exited with code $uninstallExitCode"
    }
    Wait-For -Description "the installed desktop executable removal" `
        -TimeoutSeconds 30 -Condition {
            @(
                @($desktop, $quick, $core, $installedWintun, $manager) |
                    Where-Object {
                        Test-Path -LiteralPath $_ -PathType Leaf
                    }
            ).Count -eq 0
        }
    Wait-For -Description "the management service removal" `
        -TimeoutSeconds 30 -Condition {
            $null -eq (Get-Service -Name $managerServiceName `
                -ErrorAction SilentlyContinue)
        }
    $installed = $false
    if ((Get-Service -Name $serviceName -ErrorAction Stop).Status -ne "Running") {
        throw "MSI uninstall stopped the active tunnel service"
    }
    if ($null -eq (Get-NetAdapter -Name $tunnelName `
        -ErrorAction SilentlyContinue)) {
        throw "MSI uninstall removed the active Wintun adapter"
    }
    $stagedStatus = Invoke-Native -FilePath $stagedCore -Arguments @(
        "show", $tunnelName, "--json"
    ) | ConvertFrom-Json
    if ($stagedStatus.interface -ne $tunnelName -or
        $stagedStatus.state -ne "up") {
        throw "staged runtime lost status after MSI uninstall"
    }
    if (-not (Test-Path -LiteralPath $installedConfig -PathType Leaf)) {
        throw "MSI uninstall removed the imported configuration"
    }
    Write-Host (Invoke-Native -FilePath $stagedQuick -Arguments @(
        "down", $tunnelName
    ))
    Wait-For -Description "the post-uninstall service cleanup" -Condition {
        $null -eq (Get-Service -Name $serviceName `
            -ErrorAction SilentlyContinue)
    }
    Wait-For -Description "the post-uninstall Wintun cleanup" -Condition {
        $null -eq (Get-NetAdapter -Name $tunnelName `
            -ErrorAction SilentlyContinue)
    }
    Wait-For -Description "the post-uninstall route ledger cleanup" `
        -Condition {
            if (-not (Test-Path -LiteralPath $ledgerPath -PathType Leaf)) {
                return $true
            }
            $candidateLedger = Get-Content -LiteralPath $ledgerPath -Raw |
                ConvertFrom-Json
            @(
                $candidateLedger.routes |
                    ForEach-Object { $_.owners } |
                    Where-Object { $_.tunnel -eq $tunnelName }
            ).Count -eq 0
        }
    if ($endpointRoutesBefore.Count -eq 0) {
        Wait-For -Description "the post-uninstall endpoint pin cleanup" `
            -Condition {
                $null -eq (Get-NetRoute -AddressFamily IPv4 `
                    -DestinationPrefix $endpointPrefix `
                    -ErrorAction SilentlyContinue)
            }
    }
    Write-Host "active tunnel survived MSI uninstall and cleaned up"
    Write-Host "installed Windows desktop lifecycle passed"
}
catch {
    $failure = $_
    Write-Warning "installed Windows desktop lifecycle failed: $failure"
    foreach ($diagnosticPath in @(
        $resultPath,
        $trayResultPath,
        $stdoutPath,
        $stderrPath,
        $trayStdoutPath,
        $trayStderrPath,
        $desktopIpcResult,
        $desktopIpcStdout,
        $desktopIpcStderr,
        $brokerStatusResult,
        $filteredAdminResult,
        $filteredRetiredRuntimeResult,
        $msiLogPath
    )) {
        if (Test-Path -LiteralPath $diagnosticPath -PathType Leaf) {
            Write-Host "last lines from $diagnosticPath"
            Get-Content -LiteralPath $diagnosticPath -Tail 120 |
                Write-Host
        }
    }
    throw $failure
}
finally {
    Remove-Item Env:WG_QUIC_DESKTOP_INTEGRATION_SMOKE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SMOKE_CONFIG `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SMOKE_NAME `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SMOKE_RESULT `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_TRAY_SMOKE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_ACTION `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_NAME `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SOURCE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_OVERWRITE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_PIPE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_ELEVATED_EXE `
        -ErrorAction SilentlyContinue

    foreach ($candidateProcess in @(
        $desktopProcess,
        $trayProcess,
        $secondDesktopProcess,
        $desktopIpcProbeProcess,
        $filteredProcess
    )) {
        if ($null -eq $candidateProcess) {
            continue
        }
        try {
            $candidateProcess.Refresh()
            if (-not $candidateProcess.HasExited) {
                Stop-Process -Id $candidateProcess.Id -Force `
                    -ErrorAction Stop
            }
        }
        catch {
            Write-Warning "failed to stop desktop process: $_"
        }
    }

    $cleanupQuick = if ($null -ne $stagedQuick -and
        (Test-Path -LiteralPath $stagedQuick -PathType Leaf)) {
        $stagedQuick
    }
    else {
        $quick
    }
    if ($null -ne $cleanupQuick -and
        (Test-Path -LiteralPath $cleanupQuick -PathType Leaf)) {
        foreach ($cleanupTunnel in @($tunnelName)) {
            try {
                Invoke-Native -FilePath $cleanupQuick -Arguments @(
                    "down", $cleanupTunnel, "--repair"
                ) | Write-Host
            }
            catch {
                Write-Warning $_
            }
        }
    }
    foreach ($cleanupConfig in @($installedConfig)) {
        Remove-Item -LiteralPath $cleanupConfig -Force `
            -ErrorAction SilentlyContinue
    }

    if ($filteredAdminUserCreated) {
        Remove-LocalUser -Name $filteredAdminUserName `
            -ErrorAction SilentlyContinue
    }
    if ($standardUserCreated) {
        Remove-LocalUser -Name $standardUserName `
            -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $standardUserRoot -Recurse -Force `
        -ErrorAction SilentlyContinue

    if ($installed) {
        try {
            $uninstall = Start-Process -FilePath "msiexec.exe" `
                -ArgumentList @(
                    "/x",
                    "`"$installerPath`"",
                    "/qn",
                    "/norestart"
                ) -PassThru
            $cleanupUninstallExitCode = Wait-ProcessExit `
                -Process $uninstall `
                -Description "the cleanup MSI uninstall"
            if ($cleanupUninstallExitCode -notin @(0, 3010, 1605)) {
                Write-Warning (
                    "MSI uninstall exited with code " +
                    $cleanupUninstallExitCode
                )
            }
        }
        catch {
            Write-Warning $_
        }
    }
    Remove-Item -LiteralPath $fixtureRoot -Recurse -Force `
        -ErrorAction SilentlyContinue
}
