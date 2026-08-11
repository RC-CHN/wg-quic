// Command prepare-overlay creates a narrowly scoped Go build overlay for Wine.
// Wine / Proton currently returns WSAEOPNOTSUPP for the optional UDP reset
// suppression ioctls used by Go's Windows net package. Real Windows implements
// them. Ignoring only that response lets the compatibility fixture reach the
// application data path without changing release builds.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const unsupportedCheck = `if err != nil {
			return wrapSyscallError("wsaioctl", err)
		}`

// 10045 is WSAEOPNOTSUPP. The standard syscall package doesn't export the
// Winsock-prefixed error constant.
const compatibilityCheck = `if err != nil && err != syscall.Errno(10045) {
			return wrapSyscallError("wsaioctl", err)
		}`

type overlay struct {
	Replace map[string]string
}

func main() {
	outputDirectory := flag.String("output-dir", "", "directory for the patched source and overlay")
	flag.Parse()
	if *outputDirectory == "" {
		fmt.Fprintln(os.Stderr, "-output-dir is required")
		os.Exit(2)
	}
	path, err := prepare(runtime.GOROOT(), *outputDirectory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare Wine Go overlay: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(path)
}

func prepare(goRoot, outputDirectory string) (string, error) {
	sourcePath := filepath.Join(goRoot, "src", "net", "fd_windows.go")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", sourcePath, err)
	}
	patched, err := patchWindowsNet(source)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	patchedPath := filepath.Join(outputDirectory, "fd_windows.go")
	if err := os.WriteFile(patchedPath, patched, 0o600); err != nil {
		return "", fmt.Errorf("write patched Windows net source: %w", err)
	}
	overlayPath := filepath.Join(outputDirectory, "overlay.json")
	encoded, err := json.MarshalIndent(overlay{Replace: map[string]string{sourcePath: patchedPath}}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode overlay: %w", err)
	}
	if err := os.WriteFile(overlayPath, encoded, 0o600); err != nil {
		return "", fmt.Errorf("write overlay: %w", err)
	}
	return overlayPath, nil
}

func patchWindowsNet(source []byte) ([]byte, error) {
	if count := bytes.Count(source, []byte(unsupportedCheck)); count != 2 {
		return nil, fmt.Errorf("%w: found %d matching WSAIoctl checks, want 2", errors.New("unsupported Go net source"), count)
	}
	return bytes.ReplaceAll(source, []byte(unsupportedCheck), []byte(compatibilityCheck)), nil
}
