param(
    [Parameter(Mandatory = $true)]
    [string] $Installer
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

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
$installRoot = Join-Path $env:ProgramFiles "wg-quic"
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
$quick = $null
$core = $null
$installedWintun = $null
$stagedQuick = $null
$stagedCore = $null
$installed = $false
$standardUserName = "wgqdesk$PID"
$standardUserCreated = $false
$standardUserRoot = Join-Path $env:PUBLIC "wg-quic-ci-$PID"

try {
    New-Item -ItemType Directory -Force -Path $fixtureRoot | Out-Null
    Write-Host "installing $installerPath"
    $install = Start-Process -FilePath "msiexec.exe" `
        -ArgumentList @(
            "/i",
            "`"$installerPath`"",
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
        if ($null -eq $quickItem -or $null -eq $desktopItem) {
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

    $unsafeSids = @("S-1-5-32-545", "S-1-5-11", "S-1-1-0")
    foreach ($protectedExecutable in @($desktop, $quick)) {
        $executableAcl = Get-Acl -LiteralPath $protectedExecutable
        $unsafeRule = @(
            $executableAcl.GetAccessRules(
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
            throw "$protectedExecutable is writable by unelevated users"
        }
    }
    Write-Host "installed executable ACLs are protected"

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
    $listenPort = 53000 + ($PID % 1000)
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
        "installed desktop import/UAC/service/status lifecycle passed") {
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
    $runtimeFiles = @(
        Get-ChildItem -LiteralPath (
            Join-Path $env:ProgramData "wg-quic\runtime"
        ) -Recurse -File -ErrorAction Stop |
        Where-Object {
            $_.Name -in @("wg-quic.exe", "wg-quic-quick.exe", "wintun.dll")
        }
    )
    if ($runtimeFiles.Count -lt 3) {
        throw "installed desktop did not stage a stable service runtime"
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
    $standardScript = Join-Path $standardUserRoot "status-check.ps1"
    $standardResult = Join-Path $standardUserRoot "result.txt"
    $standardStdout = Join-Path $standardUserRoot "stdout.txt"
    $standardStderr = Join-Path $standardUserRoot "stderr.txt"
    @'
param(
    [string] $Core,
    [string] $Tunnel,
    [string] $Result,
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
    $standardCredential = [Management.Automation.PSCredential]::new(
        "$env:COMPUTERNAME\$standardUserName",
        $securePassword
    )
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
    Write-Host "installed Windows standard-user status boundary passed"

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
                @($desktop, $quick, $core, $installedWintun) |
                    Where-Object {
                        Test-Path -LiteralPath $_ -PathType Leaf
                    }
            ).Count -eq 0
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

    foreach ($candidateProcess in @(
        $desktopProcess,
        $trayProcess,
        $secondDesktopProcess
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
        try {
            Invoke-Native -FilePath $cleanupQuick -Arguments @(
                "down", $tunnelName, "--repair"
            ) | Write-Host
        }
        catch {
            Write-Warning $_
        }
    }
    Remove-Item -LiteralPath $installedConfig -Force `
        -ErrorAction SilentlyContinue

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
