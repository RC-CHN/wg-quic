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
$stdoutPath = Join-Path $fixtureRoot "desktop.stdout.log"
$stderrPath = Join-Path $fixtureRoot "desktop.stderr.log"
$desktop = $null
$quick = $null
$installed = $false

try {
    New-Item -ItemType Directory -Force -Path $fixtureRoot | Out-Null
    $install = Start-Process -FilePath "msiexec.exe" `
        -ArgumentList @(
            "/i",
            "`"$installerPath`"",
            "/qn",
            "/norestart"
        ) `
        -Wait -PassThru
    if ($install.ExitCode -notin @(0, 3010)) {
        throw "MSI installer exited with code $($install.ExitCode)"
    }
    $installed = $true

    Wait-For -Description "the installed MSI application" -Condition {
        $quickItem = Get-ChildItem -LiteralPath $installRoot `
            -Recurse -File -Filter "wg-quic-quick.exe" `
            -ErrorAction SilentlyContinue |
            Where-Object {
                $_.DirectoryName.EndsWith(
                    "resources\bin",
                    [StringComparison]::OrdinalIgnoreCase
                )
            } |
            Select-Object -First 1
        if ($null -eq $quickItem) {
            return $false
        }
        $script:quick = $quickItem.FullName
        $appDirectory = $quickItem.Directory.Parent.Parent.FullName
        $script:desktop = Join-Path $appDirectory "wg-quic.exe"
        (
            (Test-Path -LiteralPath $script:desktop -PathType Leaf) -and
            (Test-Path -LiteralPath $script:quick -PathType Leaf)
        )
    }

    $unsafeSids = @("S-1-5-32-545", "S-1-5-11", "S-1-1-0")
    foreach ($protectedExecutable in @($desktop, $quick)) {
        $executableAcl = Get-Acl -LiteralPath $protectedExecutable
        $unsafeRule = @(
            $executableAcl.Access |
            Where-Object {
                $sid = $_.IdentityReference.Translate(
                    [Security.Principal.SecurityIdentifier]
                ).Value
                $_.AccessControlType -eq "Allow" -and
                $sid -in $unsafeSids -and
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

    $core = Join-Path (Split-Path -Parent $quick) "wg-quic.exe"
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
    $env:ELECTRON_ENABLE_LOGGING = "1"
    $desktopProcess = Start-Process -FilePath $desktop `
        -Wait -PassThru `
        -RedirectStandardOutput $stdoutPath `
        -RedirectStandardError $stderrPath
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
    if ($desktopOutput -notmatch
        "installed desktop import/UAC/service/status lifecycle passed") {
        throw "installed desktop did not report lifecycle success`n$desktopOutput"
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
    $uninstall = Start-Process -FilePath "msiexec.exe" `
        -ArgumentList @(
            "/x",
            "`"$installerPath`"",
            "/qn",
            "/norestart"
        ) `
        -Wait -PassThru
    if ($uninstall.ExitCode -notin @(0, 3010)) {
        throw "MSI uninstall exited with code $($uninstall.ExitCode)"
    }
    Wait-For -Description "the installed desktop executable removal" `
        -TimeoutSeconds 30 -Condition {
            -not (Test-Path -LiteralPath $desktop -PathType Leaf)
        }
    $installed = $false
    Write-Host "installed Windows desktop lifecycle passed"
}
finally {
    Remove-Item Env:WG_QUIC_DESKTOP_INTEGRATION_SMOKE `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SMOKE_CONFIG `
        -ErrorAction SilentlyContinue
    Remove-Item Env:WG_QUIC_DESKTOP_SMOKE_NAME `
        -ErrorAction SilentlyContinue
    Remove-Item Env:ELECTRON_ENABLE_LOGGING `
        -ErrorAction SilentlyContinue

    if (Get-Service -Name $serviceName -ErrorAction SilentlyContinue) {
        try {
            Invoke-Native -FilePath $quick -Arguments @(
                "down", $tunnelName, "--repair"
            ) | Write-Host
        }
        catch {
            Write-Warning $_
        }
    }
    Remove-Item -LiteralPath $installedConfig -Force `
        -ErrorAction SilentlyContinue

    if ($installed) {
        try {
            $uninstall = Start-Process -FilePath "msiexec.exe" `
                -ArgumentList @(
                    "/x",
                    "`"$installerPath`"",
                    "/qn",
                    "/norestart"
                ) `
                -Wait -PassThru
            if ($uninstall.ExitCode -notin @(0, 3010, 1605)) {
                Write-Warning "MSI uninstall exited with code $($uninstall.ExitCode)"
            }
        }
        catch {
            Write-Warning $_
        }
    }
    Remove-Item -LiteralPath $fixtureRoot -Recurse -Force `
        -ErrorAction SilentlyContinue
}
