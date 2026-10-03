//go:build windows

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const windowsNetworkCompletionPrefix = "wg-quic-network-completed:"

// One PowerShell process owns a whole apply or rollback, amortizing process,
// module and CIM initialization. Completion records retain the old rollback
// contract: only successfully applied operations are undone, in reverse order.
func runWindowsNetworkBatch(ctx context.Context, name string, scripts []string, keepGoing bool) ([]int, error) {
	if len(scripts) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	luid, err := windowsInterfaceLUID(name)
	if err != nil {
		return nil, err
	}
	return runWindowsNetworkBatchOnInterface(ctx, name, luid, scripts, keepGoing)
}

func runWindowsNetworkBatchOnInterface(ctx context.Context, name string, luid uint64, scripts []string, keepGoing bool) ([]int, error) {
	if len(scripts) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index, err := windowsInterfaceIndexFromLUID(luid)
	if err != nil {
		return nil, err
	}
	base := windowsPowerShellBase(name)
	commands := make([]string, len(scripts))
	for i, script := range scripts {
		commands[i] = strings.TrimPrefix(script, base)
	}
	// Resolve the adapter through IP Helper before starting PowerShell. Loading
	// NetAdapter and its CIM provider just to obtain this index adds seconds to
	// both startup and shutdown under normal Windows background load.
	setup := "$ifIndex=" + strconv.FormatUint(uint64(index), 10) + ";"
	return runWindowsPowerShellBatch(ctx, setup, commands, keepGoing)
}

func runWindowsPowerShellBatch(ctx context.Context, setup string, scripts []string, keepGoing bool) ([]int, error) {
	var script strings.Builder
	script.WriteString("$ErrorActionPreference='Stop';$wgFailed=$false;\ntry {\n" + setup + "\n")
	for i, operation := range scripts {
		fmt.Fprintf(&script, "$wgStep=[Diagnostics.Stopwatch]::StartNew();try { & { %s\n } | Out-Null;[Console]::Out.WriteLine('%s%d') } catch { [Console]::Error.WriteLine(('network step %d failed after {0}ms: {1}' -f $wgStep.ElapsedMilliseconds,$_.Exception.Message));$wgFailed=$true;", operation, windowsNetworkCompletionPrefix, i, i+1)
		if !keepGoing {
			script.WriteString("exit 1;")
		}
		script.WriteString("};\n")
	}
	script.WriteString("} catch { [Console]::Error.WriteLine($_.Exception.Message);exit 1 };if($wgFailed){exit 1}\n")
	// Read the script from stdin so large AllowedIPs sets cannot exceed the
	// Windows command-line limit. Explicit UTF-8 preserves non-ASCII aliases.
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command",
		"[Console]::InputEncoding=[Text.UTF8Encoding]::new($false);[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false);& ([ScriptBlock]::Create([Console]::In.ReadToEnd()))")
	cmd.Stdin = strings.NewReader(script.String())
	cmd.WaitDelay = time.Second
	output, runErr := cmd.CombinedOutput()
	var completed []int
	var details []string
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, windowsNetworkCompletionPrefix) {
			index, err := strconv.Atoi(strings.TrimPrefix(line, windowsNetworkCompletionPrefix))
			if err == nil && index >= 0 && index < len(scripts) && (len(completed) == 0 || index > completed[len(completed)-1]) {
				completed = append(completed, index)
				continue
			}
		}
		if line != "" {
			details = append(details, line)
		}
	}
	if runErr == nil && len(completed) != len(scripts) {
		runErr = fmt.Errorf("incomplete network operation result: %d of %d steps", len(completed), len(scripts))
	}
	if runErr != nil {
		if ctx.Err() != nil {
			runErr = ctx.Err()
		}
		return completed, fmt.Errorf("PowerShell network configuration: %w: %s", runErr, strings.Join(details, "\n"))
	}
	return completed, nil
}
