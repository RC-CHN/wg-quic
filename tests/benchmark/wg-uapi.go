package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const operationTimeout = 5 * time.Second

func main() {
	if len(os.Args) < 3 || len(os.Args) > 4 {
		fmt.Fprintln(os.Stderr, "usage: wg-uapi get SOCKET | wg-uapi set SOCKET CONFIG")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "get":
		if len(os.Args) != 3 {
			err = errors.New("get requires a socket path")
			break
		}
		err = get(os.Args[2])
	case "set":
		if len(os.Args) != 4 {
			err = errors.New("set requires a socket path and config path")
			break
		}
		err = set(os.Args[2], os.Args[3])
	default:
		err = fmt.Errorf("unknown operation %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func connect(socketPath string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", socketPath, operationTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", socketPath, err)
	}
	if err := conn.SetDeadline(time.Now().Add(operationTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("set UAPI deadline: %w", err)
	}
	return conn, nil
}

func get(socketPath string) error {
	conn, err := connect(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := fmt.Fprint(conn, "get=1\n\n"); err != nil {
		return fmt.Errorf("write get operation: %w", err)
	}
	lines, err := readResponse(conn)
	if err != nil {
		return err
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	return nil
}

func set(socketPath, configPath string) error {
	config, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	conn, err := connect(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	payload := strings.TrimSpace(string(config))
	if _, err := fmt.Fprintf(conn, "set=1\n%s\n\n", payload); err != nil {
		return fmt.Errorf("write set operation: %w", err)
	}
	_, err = readResponse(conn)
	return err
}

func readResponse(conn net.Conn) ([]string, error) {
	scanner := bufio.NewScanner(conn)
	var lines []string
	var errno *int
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		if value, ok := strings.CutPrefix(line, "errno="); ok {
			code, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid UAPI errno %q", value)
			}
			errno = &code
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read UAPI response: %w", err)
	}
	if errno == nil {
		return nil, errors.New("UAPI response did not contain errno")
	}
	if *errno != 0 {
		return nil, fmt.Errorf("UAPI operation failed with errno %d", *errno)
	}
	return lines, nil
}
