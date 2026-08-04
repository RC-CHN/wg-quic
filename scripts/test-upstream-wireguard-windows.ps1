$ErrorActionPreference = "Stop"

$module = "golang.zx2c4.com/wireguard"
$expectedVersion = "v0.0.0-20260522210424-ecfc5a8d5446"
$actualVersion = go list -m -f '{{.Version}}' $module
if ($LASTEXITCODE -ne 0) {
    throw "failed to resolve $module"
}
if ($actualVersion -ne $expectedVersion) {
    throw "wireguard-go version mismatch: got $actualVersion, want $expectedVersion"
}

$moduleDir = go list -m -f '{{.Dir}}' $module
if ($LASTEXITCODE -ne 0) {
    throw "failed to locate $module"
}
$testDir = Join-Path ([IO.Path]::GetTempPath()) ("wg-quic-wireguard-tests-" + [guid]::NewGuid())

try {
    Copy-Item -Recurse -Force $moduleDir $testDir

    # This pinned upstream revision's platform-neutral checksum test imports
    # x/sys/unix.IPPROTO_TCP even on Windows. The constant has the same value
    # in x/sys/windows. Patch only this known test defect in an ephemeral copy,
    # while guarding the exact source shape so an upstream change cannot be
    # hidden accidentally.
    $checksumTest = Join-Path $testDir "tun/checksum_test.go"
    (Get-Item $checksumTest).IsReadOnly = $false
    $source = Get-Content -Raw $checksumTest
    if (-not $source.Contains('"golang.org/x/sys/unix"')) {
        throw "unexpected upstream checksum test import"
    }
    if (-not $source.Contains("unix.IPPROTO_TCP")) {
        throw "expected upstream Windows checksum test defect is absent; remove the compatibility patch"
    }
    $source = $source.Replace('"golang.org/x/sys/unix"', '"golang.org/x/sys/windows"')
    $source = $source.Replace("unix.IPPROTO_TCP", "windows.IPPROTO_TCP")
    Set-Content -Path $checksumTest -Value $source -NoNewline -Encoding utf8NoBOM

    Push-Location $testDir
    try {
        Write-Host "Running every Windows-applicable upstream test from $module@$actualVersion"
        go test -count=1 ./...
        if ($LASTEXITCODE -ne 0) {
            throw "upstream wireguard-go tests failed"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    if (Test-Path $testDir) {
        Remove-Item -Recurse -Force $testDir
    }
}
