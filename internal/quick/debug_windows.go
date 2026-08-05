//go:build windows

package quick

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

func CreateWindowsDebugLog(input, requestedName string) (*os.File, string, error) {
	_, name, err := ResolveConfig(input, requestedName, platform.Current())
	if err != nil {
		return nil, "", err
	}
	root := os.Getenv("ProgramData")
	if root == "" {
		root = `C:\ProgramData`
	}
	directory := filepath.Join(root, "wg-quic", "logs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, "", fmt.Errorf("create Windows debug log directory: %w", err)
	}
	path := filepath.Join(directory, fmt.Sprintf(
		"%s-debug-%s.log", name, time.Now().Format("20060102-150405.000000000"),
	))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("create Windows debug log: %w", err)
	}
	return file, path, nil
}

func RunWindowsDebug(ctx context.Context, input, requestedName string, output io.Writer) error {
	_, name, err := ResolveConfig(input, requestedName, platform.Current())
	if err != nil {
		return err
	}
	logger := log.New(output, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	logger.Printf("debug preflight: collecting read-only Windows network state")
	writeWindowsDiagnostics(ctx, logger, name, "before tunnel")
	factory := func(launch coreLaunch) (coreProcess, error) {
		launch.Debug = true
		return newWindowsCoreProcess(launch, output)
	}
	ready := func() {
		logger.Printf("debug runtime: collecting read-only Windows network state")
		writeWindowsDiagnostics(ctx, logger, name, "tunnel running")
	}
	return runWithHostReadyLog(
		ctx, input, requestedName, platform.Current(), factory, ready,
		nil,
		runLog{logger: logger, debug: true},
	)
}

func writeWindowsDiagnostics(ctx context.Context, logger *log.Logger, name, phase string) {
	logger.Printf("debug Windows snapshot begin: phase=%q interface=%q", phase, name)
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	routeLedger := filepath.Join(programData, "wg-quic", "state", "routes-v1.json")
	script := strings.Join([]string{
		"$ErrorActionPreference='Continue'",
		"$ProgressPreference='SilentlyContinue'",
		"Write-Output ('OS: ' + [System.Environment]::OSVersion.VersionString)",
		"Write-Output ('PowerShell: ' + $PSVersionTable.PSVersion.ToString())",
		"Write-Output ('Identity: ' + [System.Security.Principal.WindowsIdentity]::GetCurrent().Name)",
		"$principal=New-Object System.Security.Principal.WindowsPrincipal([System.Security.Principal.WindowsIdentity]::GetCurrent())",
		"Write-Output ('Elevated: ' + $principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator))",
		"$adapter=Get-NetAdapter -Name " + debugPowerShellQuote(name) + " -ErrorAction SilentlyContinue",
		"Write-Output 'Adapter:'",
		"if ($null -eq $adapter) { Write-Output '(not present)' } else { $adapter | Format-List Name,InterfaceDescription,ifIndex,Status,MacAddress,LinkSpeed,DriverInformation | Out-String -Width 240 | Write-Output }",
		"Write-Output 'Interface IP/DNS:'",
		"if ($null -eq $adapter) { Write-Output '(not present)' } else { Get-NetIPConfiguration -InterfaceIndex $adapter.ifIndex -ErrorAction Continue | Format-List InterfaceAlias,InterfaceIndex,IPv4Address,IPv6Address,IPv4DefaultGateway,IPv6DefaultGateway,DNSServer | Out-String -Width 240 | Write-Output }",
		"if ($null -ne $adapter) { Get-DnsClientServerAddress -InterfaceIndex $adapter.ifIndex -ErrorAction Continue | Format-Table InterfaceAlias,AddressFamily,ServerAddresses -AutoSize | Out-String -Width 240 | Write-Output }",
		"Write-Output 'Tunnel and default routes:'",
		"Get-NetRoute -ErrorAction Continue | Where-Object { $_.InterfaceAlias -eq " + debugPowerShellQuote(name) + " -or $_.DestinationPrefix -eq '0.0.0.0/0' -or $_.DestinationPrefix -eq '::/0' } | Sort-Object AddressFamily,DestinationPrefix,RouteMetric | Format-Table AddressFamily,DestinationPrefix,NextHop,InterfaceAlias,InterfaceIndex,RouteMetric,State -AutoSize | Out-String -Width 240 | Write-Output",
		"$routeLedger=" + debugPowerShellQuote(routeLedger),
		"Write-Output ('wg-quic route ledger: ' + $routeLedger)",
		"if (Test-Path -LiteralPath $routeLedger) { $ledger=Get-Content -LiteralPath $routeLedger -Raw -ErrorAction Continue | ConvertFrom-Json -ErrorAction Continue; if ($null -ne $ledger) { $ledger.routes | Select-Object @{Name='DestinationPrefix';Expression={$_.key.destination}},@{Name='InterfaceLUID';Expression={$_.key.interfaceLuid}},InterfaceIndex,@{Name='NextHop';Expression={$_.key.nextHop}},RouteMetric,Ownership,State,Revision,@{Name='OwnerCount';Expression={@($_.owners).Count}},@{Name='References';Expression={($_.owners | Measure-Object -Property references -Sum).Sum}} | Format-Table -AutoSize | Out-String -Width 240 | Write-Output; foreach ($record in @($ledger.routes)) { Get-NetRoute -DestinationPrefix $record.key.destination -InterfaceIndex $record.interfaceIndex -ErrorAction Continue | Where-Object { $_.NextHop -eq $record.key.nextHop } | Format-Table AddressFamily,DestinationPrefix,NextHop,InterfaceAlias,InterfaceIndex,RouteMetric,Protocol,State -AutoSize | Out-String -Width 240 | Write-Output } } } else { Write-Output '(not present)' }",
		"Write-Output 'IP interface metrics:'",
		"Get-NetIPInterface -ErrorAction Continue | Where-Object { $_.ConnectionState -eq 'Connected' -or ($null -ne $adapter -and $_.InterfaceIndex -eq $adapter.ifIndex) } | Sort-Object AddressFamily,InterfaceMetric,InterfaceIndex | Format-Table AddressFamily,InterfaceAlias,InterfaceIndex,InterfaceMetric,ConnectionState,NlMtu -AutoSize | Out-String -Width 240 | Write-Output",
	}, ";")
	command := exec.CommandContext(
		ctx,
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", script,
	)
	command.Stdout = logger.Writer()
	command.Stderr = logger.Writer()
	if err := command.Run(); err != nil {
		logger.Printf("debug Windows snapshot command failed: %v", err)
	}
	logger.Printf("debug Windows snapshot end: phase=%q", phase)
}

func debugPowerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
