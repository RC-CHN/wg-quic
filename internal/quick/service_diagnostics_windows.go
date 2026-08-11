//go:build windows

package quick

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

const (
	windowsServiceFailureFileName      = "service-error.txt"
	windowsServiceDiagnosticOutputSize = 6 * 1024
	windowsServiceFailureMaxBytes      = 8 * 1024
)

type windowsServiceDiagnostics struct {
	mu        sync.Mutex
	output    []byte
	truncated bool
	writeFile func(string) error
}

func (d *windowsServiceDiagnostics) Write(contents []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	written := len(contents)
	if len(contents) >= windowsServiceDiagnosticOutputSize {
		d.output = append(
			d.output[:0],
			contents[len(contents)-windowsServiceDiagnosticOutputSize:]...,
		)
		d.truncated = true
		return written, nil
	}
	overflow := len(d.output) + len(contents) - windowsServiceDiagnosticOutputSize
	if overflow > 0 {
		copy(d.output, d.output[overflow:])
		d.output = d.output[:len(d.output)-overflow]
		d.truncated = true
	}
	d.output = append(d.output, contents...)
	return written, nil
}

func (d *windowsServiceDiagnostics) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	output := strings.TrimSpace(string(d.output))
	if d.truncated {
		output = "[earlier service output truncated]\n" + output
	}
	return output
}

func (d *windowsServiceDiagnostics) newCoreProcess(
	launch coreLaunch,
) (coreProcess, error) {
	return newWindowsCoreProcess(launch, d)
}

func (d *windowsServiceDiagnostics) runLog() runLog {
	return runLog{
		logger: log.New(d, "", log.Ldate|log.Ltime|log.Lmicroseconds),
		debug:  true,
	}
}

func (d *windowsServiceDiagnostics) recordFailure(runErr error) error {
	if runErr == nil {
		return nil
	}
	detail := d.String()
	combined := runErr
	if detail != "" {
		combined = fmt.Errorf("%w\nservice output:\n%s", runErr, detail)
	}
	writeFile := d.writeFile
	if writeFile == nil {
		writeFile = writeWindowsServiceFailureRecord
	}
	if err := writeFile(combined.Error()); err != nil {
		return errors.Join(
			combined,
			fmt.Errorf("write Windows service failure record: %w", err),
		)
	}
	return combined
}

func writeWindowsServiceFailureRecord(message string) (returnErr error) {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	directory, err := windowsRuntimeDirectoryFromExecutablePath(
		executable,
		"wg-quic-quick.exe",
	)
	if err != nil {
		return err
	}
	directoryLease, err := openWindowsTrustedRuntimeDirectory(directory)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, directoryLease.Close())
	}()
	message = sanitizeWindowsServiceDiagnostic(
		message,
		windowsServiceFailureMaxBytes,
	)
	path := filepath.Join(directory, windowsServiceFailureFileName)
	file, err := createWindowsSecureFile(path, "service failure record")
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	if _, err := io.WriteString(file, message); err != nil {
		return err
	}
	return file.Sync()
}

func readWindowsServiceFailureRecord(
	executable string,
) (detail string, returnErr error) {
	directory, err := windowsRuntimeDirectoryFromExecutablePath(
		executable,
		"wg-quic-quick.exe",
	)
	if err != nil {
		return "", err
	}
	directoryLease, err := openWindowsTrustedRuntimeDirectory(directory)
	if err != nil {
		return "", err
	}
	defer func() {
		returnErr = errors.Join(returnErr, directoryLease.Close())
	}()
	path := filepath.Join(directory, windowsServiceFailureFileName)
	file, err := openWindowsSecureExistingFile(path, "service failure record")
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	contents, err := io.ReadAll(io.LimitReader(
		file,
		windowsServiceFailureMaxBytes+1,
	))
	if err != nil {
		return "", err
	}
	if len(contents) > windowsServiceFailureMaxBytes {
		return "", fmt.Errorf(
			"Windows service failure record exceeds %d bytes",
			windowsServiceFailureMaxBytes,
		)
	}
	return strings.TrimSpace(sanitizeWindowsServiceDiagnostic(
		string(contents),
		windowsServiceFailureMaxBytes,
	)), nil
}

func sanitizeWindowsServiceDiagnostic(message string, limit int) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	message = strings.Map(func(character rune) rune {
		switch character {
		case '\n', '\r', '\t':
			return character
		}
		if unicode.IsControl(character) {
			return '\uFFFD'
		}
		return character
	}, message)
	if limit <= 0 {
		return ""
	}
	if len(message) <= limit {
		return message
	}
	const marker = "[earlier diagnostic text truncated]\n"
	if limit <= len(marker) {
		return marker[:limit]
	}
	start := len(message) - (limit - len(marker))
	for start < len(message) && !utf8.RuneStart(message[start]) {
		start++
	}
	return marker + message[start:]
}
