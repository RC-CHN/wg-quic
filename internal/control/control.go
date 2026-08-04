package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
)

type Status struct {
	Interface  string          `json:"interface"`
	State      string          `json:"state"`
	ListenPort uint16          `json:"listen_port"`
	Carrier    string          `json:"carrier"`
	FECMode    string          `json:"fec_mode"`
	ObfsMode   string          `json:"obfs_mode"`
	Stats      armorbind.Stats `json:"stats"`
}

type Server struct {
	listener net.Listener
	path     string
	done     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

func SocketPath(name string) string {
	return filepath.Join("/run/wg-quic", name+".sock")
}

func Start(ctx context.Context, path string, status func() Status) (*Server, error) {
	if status == nil {
		return nil, errors.New("status provider is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket path %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, err
	}
	server := &Server{listener: listener, path: path, done: make(chan struct{})}
	server.wg.Add(1)
	go server.accept(status)
	go func() {
		select {
		case <-ctx.Done():
			server.Close()
		case <-server.done:
		}
	}()
	return server, nil
}

func (s *Server) accept(status func() Status) {
	defer s.wg.Done()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer connection.Close()
			_ = json.NewEncoder(connection).Encode(status())
		}()
	}
}

func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		err = s.listener.Close()
		s.wg.Wait()
		if removeErr := os.Remove(s.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
			err = removeErr
		}
		close(s.done)
	})
	return err
}

func Read(path string) (Status, error) {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	connection, err := dialer.Dial("unix", path)
	if err != nil {
		return Status{}, err
	}
	defer connection.Close()
	var status Status
	if err := json.NewDecoder(connection).Decode(&status); err != nil {
		return Status{}, err
	}
	return status, nil
}
