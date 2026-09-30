//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWindowsPowerShellBatchStopsApplyButContinuesRollback(t *testing.T) {
	for _, keepGoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "rollback"}[keepGoing], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "steps.txt")
			appendStep := func(step string) string {
				return "[IO.File]::AppendAllText(" + powerShellQuote(path) + "," + powerShellQuote(step) + ")"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			completed, err := runWindowsPowerShellBatch(ctx, "", []string{appendStep("a"), "throw 'fixture failure'", appendStep("c")}, keepGoing)
			if err == nil || !strings.Contains(err.Error(), "network step 2 failed") || !strings.Contains(err.Error(), "fixture failure") {
				t.Fatalf("lost step error: %v", err)
			}
			wantSteps, wantCompleted := "a", []int{0}
			if keepGoing {
				wantSteps, wantCompleted = "ac", []int{0, 2}
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil || string(contents) != wantSteps || !reflect.DeepEqual(completed, wantCompleted) {
				t.Fatalf("steps=%q completed=%v read=%v", contents, completed, readErr)
			}
		})
	}
}

func TestWindowsNetworkBatchResolvesAdapterOnce(t *testing.T) {
	// Real PowerShell, stubbed network cmdlets: no host network mutations.
	setup := "$script:calls=0;function Get-NetAdapter {param($Name) $script:calls++;if($Name -ne '办公 VPN'){throw 'alias encoding'};[pscustomobject]@{ifIndex=42}};"
	base := windowsPowerShellBase("办公 VPN")
	scripts := []string{
		"if($ifIndex -ne 42){throw 'missing interface'}",
		"if($script:calls -ne 1){throw 'repeated adapter lookup'}",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	completed, err := runWindowsPowerShellBatch(ctx, setup+base, scripts, false)
	if err != nil || !reflect.DeepEqual(completed, []int{0, 1}) {
		t.Fatalf("completed=%v err=%v", completed, err)
	}
}

func TestWindowsPowerShellBatchCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runWindowsPowerShellBatch(ctx, "", []string{"Start-Sleep -Seconds 30"}, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation=%v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancelled PowerShell retained the output pipe")
	}
}
