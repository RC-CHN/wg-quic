package config

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/peerendpoint"
)

type Config struct {
	Interface Interface
	Peers     []Peer
	Transport Transport
}

type Interface struct {
	PrivateKey string
	Addresses  []netip.Prefix
	ListenPort uint16
	FwMark     uint32
	DNS        []string
	MTU        int
	Table      string
	PreUp      []string
	PostUp     []string
	PreDown    []string
	PostDown   []string
	SaveConfig bool
}

type Peer struct {
	PublicKey           string
	PresharedKey        string
	AllowedIPs          []netip.Prefix
	Endpoint            string
	PersistentKeepalive uint16
	FECPolicy           string
}

type Transport struct {
	Carrier       string
	Congestion    string
	FEC           string
	Obfs          string
	FECDataShards int
}

type DNSSettings struct {
	Servers []string
	Domains []string
}

const DefaultMTU = 1280

// EffectiveMTU is the single policy source for the configured or automatic
// tunnel MTU. Platform code may apply this value to distinct OS surfaces, but
// must not choose its own fallback.
func (cfg *Config) EffectiveMTU() int {
	if cfg != nil && cfg.Interface.MTU != 0 {
		return cfg.Interface.MTU
	}
	return DefaultMTU
}

// ClassifyDNS implements the shared wg-quick DNS field semantics. Platform
// backends own how these two projections are applied, not how they are parsed.
func ClassifyDNS(values []string) DNSSettings {
	var result DNSSettings
	for _, value := range values {
		if _, err := netip.ParseAddr(value); err == nil {
			result.Servers = append(result.Servers, value)
		} else {
			result.Domains = append(result.Domains, value)
		}
	}
	return result
}

func (cfg *Config) Clone() *Config {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	clone.Interface.Addresses = append([]netip.Prefix(nil), cfg.Interface.Addresses...)
	clone.Interface.DNS = append([]string(nil), cfg.Interface.DNS...)
	clone.Interface.PreUp = append([]string(nil), cfg.Interface.PreUp...)
	clone.Interface.PostUp = append([]string(nil), cfg.Interface.PostUp...)
	clone.Interface.PreDown = append([]string(nil), cfg.Interface.PreDown...)
	clone.Interface.PostDown = append([]string(nil), cfg.Interface.PostDown...)
	clone.Peers = append([]Peer(nil), cfg.Peers...)
	for index := range clone.Peers {
		clone.Peers[index].AllowedIPs = append([]netip.Prefix(nil), cfg.Peers[index].AllowedIPs...)
	}
	return &clone
}

func DefaultTransport() Transport {
	return Transport{Carrier: "quic", Congestion: "auto", FEC: "auto", Obfs: "salamander"}
}

func ParseFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func Parse(r io.Reader) (*Config, error) {
	cfg := &Config{Transport: DefaultTransport()}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var section string
	var peer *Peer
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "# wg-quic:") {
			directive := strings.TrimSpace(strings.TrimPrefix(raw, "# wg-quic:"))
			if err := parseDirective(cfg, peer, directive); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			continue
		}
		if strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, ";") {
			continue
		}
		if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
			switch strings.ToLower(strings.TrimSpace(raw[1 : len(raw)-1])) {
			case "interface":
				section, peer = "interface", nil
			case "peer":
				section = "peer"
				cfg.Peers = append(cfg.Peers, Peer{})
				peer = &cfg.Peers[len(cfg.Peers)-1]
			default:
				return nil, fmt.Errorf("line %d: unknown section %q", lineNo, raw)
			}
			continue
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected key = value", lineNo)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		var err error
		switch section {
		case "interface":
			err = parseInterface(&cfg.Interface, key, value)
		case "peer":
			err = parsePeer(peer, key, value)
		default:
			err = errors.New("setting appears before a section")
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func parseInterface(iface *Interface, key, value string) error {
	switch strings.ToLower(key) {
	case "privatekey":
		iface.PrivateKey = value
	case "address":
		prefixes, err := parseAddresses(value)
		if err != nil {
			return fmt.Errorf("Address: %w", err)
		}
		iface.Addresses = append(iface.Addresses, prefixes...)
	case "listenport":
		port, err := parseUint16(value)
		if err != nil {
			return fmt.Errorf("ListenPort: %w", err)
		}
		iface.ListenPort = port
	case "fwmark":
		mark, err := strconv.ParseUint(value, 0, 32)
		if err != nil {
			return fmt.Errorf("FwMark: %w", err)
		}
		iface.FwMark = uint32(mark)
	case "dns":
		iface.DNS = append(iface.DNS, splitList(value)...)
	case "mtu":
		mtu, err := strconv.Atoi(value)
		if err != nil || mtu < 576 || mtu > 65535 {
			return fmt.Errorf("MTU must be between 576 and 65535")
		}
		iface.MTU = mtu
	case "table":
		iface.Table = value
	case "preup":
		iface.PreUp = append(iface.PreUp, value)
	case "postup":
		iface.PostUp = append(iface.PostUp, value)
	case "predown":
		iface.PreDown = append(iface.PreDown, value)
	case "postdown":
		iface.PostDown = append(iface.PostDown, value)
	case "saveconfig":
		v, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("SaveConfig: %w", err)
		}
		iface.SaveConfig = v
	default:
		return fmt.Errorf("unknown Interface key %q", key)
	}
	return nil
}

func parsePeer(peer *Peer, key, value string) error {
	if peer == nil {
		return errors.New("peer setting without Peer section")
	}
	switch strings.ToLower(key) {
	case "publickey":
		peer.PublicKey = value
	case "presharedkey":
		peer.PresharedKey = value
	case "allowedips":
		prefixes, err := parsePrefixes(value)
		if err != nil {
			return fmt.Errorf("AllowedIPs: %w", err)
		}
		peer.AllowedIPs = append(peer.AllowedIPs, prefixes...)
	case "endpoint":
		peer.Endpoint = value
	case "persistentkeepalive":
		if strings.EqualFold(value, "off") {
			peer.PersistentKeepalive = 0
		} else {
			v, err := parseUint16(value)
			if err != nil {
				return fmt.Errorf("PersistentKeepalive: %w", err)
			}
			peer.PersistentKeepalive = v
		}
	default:
		return fmt.Errorf("unknown Peer key %q", key)
	}
	return nil
}

func parseDirective(cfg *Config, peer *Peer, directive string) error {
	key, value, ok := strings.Cut(directive, "=")
	if !ok {
		return fmt.Errorf("invalid wg-quic directive %q", directive)
	}
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("empty wg-quic directive %q", key)
	}
	switch key {
	case "carrier":
		if value != "quic" {
			return fmt.Errorf("unsupported carrier %q", value)
		}
		cfg.Transport.Carrier = value
	case "congestion":
		if value != "auto" && value != "reno" && value != "cubic" && value != "model" {
			return fmt.Errorf("unsupported congestion mode %q", value)
		}
		cfg.Transport.Congestion = value
	case "fec":
		if value != "auto" && value != "off" {
			return fmt.Errorf("unsupported fec mode %q", value)
		}
		cfg.Transport.FEC = value
	case "fec-data-shards":
		// MaxDataShards (32) is the wire-format ceiling and the deadline-based
		// encoder shrinks the actual group size on low-rate links, so the
		// configured value is an upper bound rather than a fixed block length.
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 32 {
			return fmt.Errorf("fec-data-shards must be between 1 and 32, got %q", value)
		}
		cfg.Transport.FECDataShards = n
	case "obfs":
		if value != "none" && value != "salamander" {
			return fmt.Errorf("unsupported obfs profile %q", value)
		}
		cfg.Transport.Obfs = value
	case "peer.fec-latency":
		if peer == nil {
			return errors.New("peer.fec-latency must be inside a Peer section")
		}
		switch value {
		case "latency", "balanced", "throughput":
			peer.FECPolicy = value
		default:
			return fmt.Errorf("unsupported peer.fec-latency %q", value)
		}
	default:
		return fmt.Errorf("unknown wg-quic directive %q", key)
	}
	return nil
}

func (cfg *Config) Validate() error {
	if _, err := decodeKey(cfg.Interface.PrivateKey); err != nil {
		return fmt.Errorf("Interface PrivateKey: %w", err)
	}
	seen := make(map[string]struct{}, len(cfg.Peers))
	for i := range cfg.Peers {
		peer := &cfg.Peers[i]
		if _, err := decodeKey(peer.PublicKey); err != nil {
			return fmt.Errorf("Peer %d PublicKey: %w", i+1, err)
		}
		if _, ok := seen[peer.PublicKey]; ok {
			return fmt.Errorf("Peer %d PublicKey is duplicated", i+1)
		}
		seen[peer.PublicKey] = struct{}{}
		if peer.PresharedKey != "" {
			if _, err := decodeKey(peer.PresharedKey); err != nil {
				return fmt.Errorf("Peer %d PresharedKey: %w", i+1, err)
			}
		}
		if peer.Endpoint != "" {
			if _, err := peerendpoint.Parse(peer.Endpoint); err != nil {
				return fmt.Errorf("Peer %d Endpoint: %w", i+1, err)
			}
		}
	}
	return nil
}

func (cfg *Config) UAPI() (string, error) {
	var out bytes.Buffer
	privateKey, err := keyHex(cfg.Interface.PrivateKey)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&out, "private_key=%s\nlisten_port=%d\nreplace_peers=true\n", privateKey, cfg.Interface.ListenPort)
	if cfg.Interface.FwMark != 0 {
		fmt.Fprintf(&out, "fwmark=%d\n", cfg.Interface.FwMark)
	}
	for _, peer := range cfg.Peers {
		publicKey, err := keyHex(peer.PublicKey)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "public_key=%s\nprotocol_version=1\n", publicKey)
		if peer.PresharedKey != "" {
			psk, err := keyHex(peer.PresharedKey)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&out, "preshared_key=%s\n", psk)
		}
		if peer.Endpoint != "" {
			fmt.Fprintf(&out, "endpoint=%s\n", peer.Endpoint)
		}
		fmt.Fprintf(&out, "persistent_keepalive_interval=%d\nreplace_allowed_ips=true\n", peer.PersistentKeepalive)
		for _, prefix := range peer.AllowedIPs {
			fmt.Fprintf(&out, "allowed_ip=%s\n", prefix)
		}
	}
	return out.String(), nil
}

func decodeKey(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("is required")
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("must be base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("must decode to 32 bytes, got %d", len(key))
	}
	return key, nil
}

func keyHex(value string) (string, error) {
	key, err := decodeKey(value)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

func parsePrefixes(value string) ([]netip.Prefix, error) {
	values := splitList(value)
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, item := range values {
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func parseAddresses(value string) ([]netip.Prefix, error) {
	values := splitList(value)
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, item := range values {
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, err
		}
		// Interface addresses carry both the host address and its prefix
		// length. Unlike AllowedIPs, masking them changes their meaning.
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func splitList(value string) []string {
	fields := strings.Split(value, ",")
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			result = append(result, field)
		}
	}
	return result
}

func parseUint16(value string) (uint16, error) {
	v, err := strconv.ParseUint(value, 10, 16)
	return uint16(v), err
}
