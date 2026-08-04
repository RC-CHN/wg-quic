// Package quic owns the QUIC carrier below ArmorBind.
//
// quic-go, TLS, UDP socket setup, socket marks, and the optional Salamander
// PacketConn are kept here so the WireGuard bind adapter only handles opaque
// datagrams and endpoint/session policy.
package quic

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	quicgo "github.com/quic-go/quic-go"
)

const alpn = "wg-quic/1"

type Config struct {
	HandshakeTimeout time.Duration
	MaxIdleTimeout   time.Duration
	KeepAlivePeriod  time.Duration
	Mark             uint32
	ObfsMode         string
	ObfsKeys         []obfs.Key
	EndpointKeys     map[netip.AddrPort]obfs.Key
}

type Carrier struct {
	listener   *quicgo.Listener
	transport  *quicgo.Transport
	packetConn *net.UDPConn
	obfsConn   *obfs.SalamanderConn
	cfg        Config
}

// obfuscatedConn deliberately exposes only net.PacketConn plus socket buffer
// tuning to quic-go. Salamander changes datagram length, so raw UDP GSO/GRO
// metadata must stay hidden until segment-aware rewriting is implemented.
type obfuscatedConn struct {
	net.PacketConn
	udp *net.UDPConn
}

func (c *obfuscatedConn) SetReadBuffer(bytes int) error {
	return c.udp.SetReadBuffer(bytes)
}

func (c *obfuscatedConn) SetWriteBuffer(bytes int) error {
	return c.udp.SetWriteBuffer(bytes)
}

func Open(port uint16, cfg Config) (*Carrier, error) {
	cfg.ObfsKeys = append([]obfs.Key(nil), cfg.ObfsKeys...)
	endpointKeys := make(map[netip.AddrPort]obfs.Key, len(cfg.EndpointKeys))
	for endpoint, key := range cfg.EndpointKeys {
		endpointKeys[endpoint] = key
	}
	cfg.EndpointKeys = endpointKeys
	tlsConfig, err := serverTLSConfig()
	if err != nil {
		return nil, err
	}
	rawConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: int(port)})
	if err != nil {
		return nil, err
	}
	if cfg.Mark != 0 {
		if err := setSocketMark(rawConn, cfg.Mark); err != nil {
			rawConn.Close()
			return nil, err
		}
	}
	var transportConn net.PacketConn = rawConn
	var obfsConn *obfs.SalamanderConn
	switch cfg.ObfsMode {
	case "none":
	case "salamander":
		peers := make([]obfs.PeerKey, 0, len(cfg.ObfsKeys)+len(cfg.EndpointKeys))
		for _, key := range cfg.ObfsKeys {
			peers = append(peers, obfs.PeerKey{Key: key})
		}
		for endpoint, key := range cfg.EndpointKeys {
			peers = append(peers, obfs.PeerKey{Key: key, Endpoint: endpoint})
		}
		obfsConn, err = obfs.WrapKeyedSalamander(rawConn, peers)
		if err != nil {
			rawConn.Close()
			return nil, err
		}
		transportConn = &obfuscatedConn{PacketConn: obfsConn, udp: rawConn}
	default:
		rawConn.Close()
		return nil, fmt.Errorf("unsupported obfuscation mode %q", cfg.ObfsMode)
	}
	transport := &quicgo.Transport{Conn: transportConn}
	listener, err := transport.Listen(tlsConfig, quicConfig(cfg))
	if err != nil {
		transport.Close()
		return nil, err
	}
	return &Carrier{
		listener: listener, transport: transport,
		packetConn: rawConn, obfsConn: obfsConn, cfg: cfg,
	}, nil
}

type Connection struct {
	conn *quicgo.Conn
}

func (c *Connection) SendDatagram(packet []byte) error {
	return c.conn.SendDatagram(packet)
}

func (c *Connection) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return c.conn.ReceiveDatagram(ctx)
}

func (c *Connection) CloseWithError(message string) error {
	return c.conn.CloseWithError(0, message)
}

func (c *Connection) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *Carrier) Accept(ctx context.Context) (*Connection, netip.AddrPort, error) {
	connection, err := c.listener.Accept(ctx)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	remote, err := addrPort(connection.RemoteAddr())
	if err != nil {
		connection.CloseWithError(1, err.Error())
		return nil, netip.AddrPort{}, err
	}
	return &Connection{conn: connection}, remote, nil
}

func (c *Carrier) Dial(ctx context.Context, remote netip.AddrPort) (*Connection, error) {
	connection, err := c.transport.Dial(
		ctx,
		net.UDPAddrFromAddrPort(remote),
		&tls.Config{
			InsecureSkipVerify: true, // Ephemeral outer TLS; WireGuard authenticates peers.
			NextProtos:         []string{alpn},
			MinVersion:         tls.VersionTLS13,
		},
		quicConfig(c.cfg),
	)
	if err != nil {
		return nil, err
	}
	return &Connection{conn: connection}, nil
}

func (c *Carrier) Port() uint16 {
	return uint16(c.packetConn.LocalAddr().(*net.UDPAddr).Port)
}

func (c *Carrier) SetMark(mark uint32) error {
	return setSocketMark(c.packetConn, mark)
}

func (c *Carrier) AssociateEndpoint(endpoint netip.AddrPort, key obfs.Key) {
	if c.obfsConn != nil {
		c.obfsConn.AssociateEndpoint(endpoint, key)
	}
}

func (c *Carrier) DisassociateEndpoint(endpoint netip.AddrPort, expected obfs.Key) {
	if c.obfsConn != nil {
		c.obfsConn.DisassociateEndpoint(endpoint, expected)
	}
}

// AbortNetwork closes only the underlying UDP socket. It models abrupt path
// loss without sending a graceful QUIC close and is used by restart tests.
func (c *Carrier) AbortNetwork() error {
	return c.packetConn.Close()
}

func (c *Carrier) Close() error {
	listenerErr := c.listener.Close()
	transportErr := c.transport.Close()
	if listenerErr != nil && !errors.Is(listenerErr, net.ErrClosed) {
		return listenerErr
	}
	if transportErr != nil && !errors.Is(transportErr, net.ErrClosed) {
		return transportErr
	}
	return nil
}

func quicConfig(cfg Config) *quicgo.Config {
	return &quicgo.Config{
		EnableDatagrams: true, HandshakeIdleTimeout: cfg.HandshakeTimeout,
		MaxIdleTimeout: cfg.MaxIdleTimeout, KeepAlivePeriod: cfg.KeepAlivePeriod,
		MaxIncomingStreams: -1, MaxIncomingUniStreams: -1,
	}
}

func serverTLSConfig() (*tls.Config, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), NotBefore: now.Add(-time.Minute),
		NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, privateKey.Public(), privateKey)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{certDER}, PrivateKey: privateKey}},
		NextProtos:   []string{alpn}, MinVersion: tls.VersionTLS13,
	}, nil
}

func addrPort(addr net.Addr) (netip.AddrPort, error) {
	udp, ok := addr.(*net.UDPAddr)
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("unexpected remote address %T", addr)
	}
	ip, ok := netip.AddrFromSlice(udp.IP)
	if !ok {
		return netip.AddrPort{}, errors.New("remote address has invalid IP")
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(udp.Port)), nil
}
