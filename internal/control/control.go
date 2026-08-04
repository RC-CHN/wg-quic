package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
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
	done     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
	cleanup  func() error
}

func Start(ctx context.Context, path string, status func() Status) (*Server, error) {
	if status == nil {
		return nil, errors.New("status provider is required")
	}
	listener, cleanup, err := listen(path)
	if err != nil {
		return nil, err
	}
	server := &Server{
		listener: listener,
		done:     make(chan struct{}),
		cleanup:  cleanup,
	}
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
		if s.cleanup != nil {
			err = errors.Join(err, s.cleanup())
		}
		close(s.done)
	})
	return err
}

func Read(path string) (Status, error) {
	connection, err := dial(path, 2*time.Second)
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
