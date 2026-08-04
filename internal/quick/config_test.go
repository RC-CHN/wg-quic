package quick

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
)

type testHost struct {
	mu          sync.Mutex
	events      []string
	configRoot  string
	controlRoot string
	postUp      chan struct{}
}

func (h *testHost) record(event string) {
	h.mu.Lock()
	h.events = append(h.events, event)
	h.mu.Unlock()
}

func (h *testHost) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.events)
}

func (h *testHost) ValidateInterfaceName(name string) error {
	if name == "" || len(name) > 15 {
		return fmt.Errorf("invalid test interface name %q", name)
	}
	return nil
}

func (h *testHost) ControlPath(name string) string {
	return filepath.Join(h.controlRoot, name+".sock")
}

func (h *testHost) ConfigPath(name string) string {
	return filepath.Join(h.configRoot, name+".conf")
}

func (h *testHost) Prepare(context.Context, *config.Config) error {
	h.record("prepare")
	return nil
}

func (h *testHost) CreateTUN(name string, mtu int) (tun.Device, error) {
	return nil, errors.New("quick must not create the core TUN")
}

func (h *testHost) ConfigureNetwork(context.Context, string, *config.Config) (platform.Cleanup, error) {
	h.record("network-up")
	return func(context.Context) error {
		h.record("network-down")
		return nil
	}, nil
}

func (h *testHost) RunHook(ctx context.Context, hook, name string) error {
	h.record(hook)
	if hook == "post-up" {
		close(h.postUp)
	}
	return nil
}

type testCoreProcess struct {
	host     *testHost
	name     string
	done     chan struct{}
	stopOnce sync.Once
	server   *control.Server
	err      error
}

func (p *testCoreProcess) Start() error {
	p.host.record("core-start")
	server, err := control.Start(context.Background(), p.host.ControlPath(p.name), func() control.Status {
		return control.Status{Interface: p.name, State: "up"}
	})
	if err != nil {
		return err
	}
	p.server = server
	return nil
}

func (p *testCoreProcess) Stop() error {
	p.stopOnce.Do(func() {
		p.host.record("core-stop")
		p.err = p.server.Close()
		close(p.done)
	})
	return p.err
}

func (p *testCoreProcess) Done() <-chan struct{} {
	return p.done
}

func (p *testCoreProcess) Err() error {
	return p.err
}

func TestResolveConfigSupportsInterfaceAndExplicitPath(t *testing.T) {
	host := &testHost{configRoot: "/configs"}
	path, name, err := ResolveConfig("wg0", "", host)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join("/configs", "wg0.conf") || name != "wg0" {
		t.Fatalf("bare interface resolved to path=%q name=%q", path, name)
	}
	path, name, err = ResolveConfig("./custom.conf", "tun7", host)
	if err != nil {
		t.Fatal(err)
	}
	if path != "./custom.conf" || name != "tun7" {
		t.Fatalf("explicit path resolved to path=%q name=%q", path, name)
	}
}

func TestQuickRejectsUnsupportedSaveConfig(t *testing.T) {
	err := validateConfig(&config.Config{Interface: config.Interface{SaveConfig: true}})
	if err == nil {
		t.Fatal("SaveConfig=true was silently accepted")
	}
}

func TestQuickOwnsHostPolicyAroundCoreLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "wg0.conf")
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	contents := fmt.Sprintf(`[Interface]
PrivateKey = %s
PreUp = pre-up
PostUp = post-up
PreDown = pre-down
PostDown = post-down
`, key)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	host := &testHost{
		configRoot: tempDir, controlRoot: tempDir,
		postUp: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runWithHost(ctx, path, "wg0", host, func(configPath, name string, fwmark uint32) (coreProcess, error) {
			return &testCoreProcess{host: host, name: name, done: make(chan struct{})}, nil
		})
	}()
	select {
	case <-host.postUp:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("quick layer did not finish bringing the core up")
	}
	select {
	case err := <-result:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quick layer did not shut down")
	}
	want := []string{
		"prepare", "pre-up", "core-start", "network-up", "post-up",
		"pre-down", "network-down", "post-down", "core-stop",
	}
	if got := host.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("quick lifecycle events = %#v, want %#v", got, want)
	}
}
