// Package reconcile owns the platform-neutral desired-state and transaction
// semantics used by wg-quic-quick runtime peer reconciliation.
package reconcile

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/peerendpoint"
)

const DefaultFECPolicy = "balanced"

// Desired is one canonical, validated quick configuration. Config returns a
// clone so callers cannot mutate state after it has been fingerprinted.
type Desired struct {
	config          *config.Config
	peerKeyBytes    map[string][32]byte
	nonSecretDigest string
}

// FromConfig canonicalizes cfg. When active is non-nil, an omitted preshared
// key for an existing peer retains the active key as required by the runtime
// reconciliation contract.
func FromConfig(cfg *config.Config, active *Desired) (*Desired, error) {
	if cfg == nil {
		return nil, errors.New("configuration is required")
	}
	candidate := cfg.Clone()
	if err := canonicalizeInterface(candidate); err != nil {
		return nil, err
	}
	if err := canonicalizeTransport(candidate); err != nil {
		return nil, err
	}
	peerKeys, err := canonicalizePeers(candidate, active)
	if err != nil {
		return nil, err
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	digest, err := digestNonSecret(candidate)
	if err != nil {
		return nil, err
	}
	return &Desired{
		config: candidate, peerKeyBytes: peerKeys, nonSecretDigest: digest,
	}, nil
}

func (d *Desired) Config() *config.Config {
	if d == nil || d.config == nil {
		return nil
	}
	return d.config.Clone()
}

func (d *Desired) Digest() string {
	if d == nil {
		return ""
	}
	return d.nonSecretDigest
}

func (d *Desired) clone() *Desired {
	if d == nil {
		return nil
	}
	peerKeys := make(map[string][32]byte, len(d.peerKeyBytes))
	for publicKey, decoded := range d.peerKeyBytes {
		peerKeys[publicKey] = decoded
	}
	return &Desired{
		config: d.config.Clone(), peerKeyBytes: peerKeys,
		nonSecretDigest: d.nonSecretDigest,
	}
}

func canonicalizeInterface(cfg *config.Config) error {
	privateKey, err := canonicalKey(cfg.Interface.PrivateKey)
	if err != nil {
		return fmt.Errorf("Interface PrivateKey: %w", err)
	}
	cfg.Interface.PrivateKey = privateKey
	if cfg.Interface.MTU == 0 {
		cfg.Interface.MTU = cfg.EffectiveMTU()
	}
	cfg.Interface.Table = strings.ToLower(strings.TrimSpace(cfg.Interface.Table))
	if cfg.Interface.Table == "" {
		cfg.Interface.Table = "auto"
	}

	addresses := append([]netip.Prefix(nil), cfg.Interface.Addresses...)
	seenAddresses := make(map[netip.Prefix]struct{}, len(addresses))
	for _, address := range addresses {
		if !address.IsValid() {
			return errors.New("Interface Address is invalid")
		}
		if _, exists := seenAddresses[address]; exists {
			return fmt.Errorf("Interface Address %s is duplicated", address)
		}
		seenAddresses[address] = struct{}{}
	}
	sortPrefixes(addresses)
	cfg.Interface.Addresses = addresses

	for index, value := range cfg.Interface.DNS {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("Interface DNS %d is empty", index+1)
		}
		if address, err := netip.ParseAddr(value); err == nil {
			cfg.Interface.DNS[index] = address.Unmap().String()
			continue
		}
		prefix := ""
		if strings.HasPrefix(value, "~") {
			prefix, value = "~", strings.TrimPrefix(value, "~")
		}
		if prefix == "~" && value == "." {
			cfg.Interface.DNS[index] = "~."
			continue
		}
		host, err := canonicalHostname(value)
		if err != nil {
			return fmt.Errorf("Interface DNS %d: %w", index+1, err)
		}
		cfg.Interface.DNS[index] = prefix + host
	}
	return nil
}

func canonicalizeTransport(cfg *config.Config) error {
	defaults := config.DefaultTransport()
	if cfg.Transport.Carrier == "" {
		cfg.Transport.Carrier = defaults.Carrier
	}
	if cfg.Transport.Congestion == "" {
		cfg.Transport.Congestion = defaults.Congestion
	}
	if cfg.Transport.FEC == "" {
		cfg.Transport.FEC = defaults.FEC
	}
	if cfg.Transport.Obfs == "" {
		cfg.Transport.Obfs = defaults.Obfs
	}
	if cfg.Transport.Carrier != "quic" {
		return fmt.Errorf("unsupported carrier %q", cfg.Transport.Carrier)
	}
	switch cfg.Transport.Congestion {
	case "auto", "reno", "cubic", "model":
	default:
		return fmt.Errorf("unsupported congestion mode %q", cfg.Transport.Congestion)
	}
	if cfg.Transport.FEC != "auto" && cfg.Transport.FEC != "off" {
		return fmt.Errorf("unsupported FEC mode %q", cfg.Transport.FEC)
	}
	if cfg.Transport.Obfs != "none" && cfg.Transport.Obfs != "salamander" {
		return fmt.Errorf("unsupported obfuscation mode %q", cfg.Transport.Obfs)
	}
	if cfg.Transport.FECDataShards < 0 || cfg.Transport.FECDataShards > 32 {
		return fmt.Errorf("FEC data shards must be between 0 and 32")
	}
	if cfg.Transport.FECInterleave < 0 || cfg.Transport.FECInterleave > 4 {
		return fmt.Errorf("FEC interleave must be between 0 and 4")
	}
	return nil
}

func canonicalizePeers(cfg *config.Config, active *Desired) (map[string][32]byte, error) {
	activePeers := make(map[string]config.Peer)
	if active != nil {
		for _, peer := range active.config.Peers {
			activePeers[peer.PublicKey] = peer
		}
	}

	peerKeys := make(map[string][32]byte, len(cfg.Peers))
	prefixOwners := make(map[netip.Prefix]string)
	for index := range cfg.Peers {
		peer := &cfg.Peers[index]
		publicKey, decoded, err := canonicalPublicKey(peer.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("Peer %d PublicKey: %w", index+1, err)
		}
		if _, exists := peerKeys[publicKey]; exists {
			return nil, fmt.Errorf("Peer %d PublicKey is duplicated", index+1)
		}
		peer.PublicKey = publicKey
		peerKeys[publicKey] = decoded

		if peer.PresharedKey == "" {
			if current, exists := activePeers[publicKey]; exists {
				peer.PresharedKey = current.PresharedKey
			}
		} else {
			peer.PresharedKey, err = canonicalKey(peer.PresharedKey)
			if err != nil {
				return nil, fmt.Errorf("Peer %d PresharedKey: %w", index+1, err)
			}
		}

		if peer.Endpoint != "" {
			peer.Endpoint, err = canonicalEndpoint(peer.Endpoint)
			if err != nil {
				return nil, fmt.Errorf("Peer %d Endpoint: %w", index+1, err)
			}
		}

		if peer.FECPolicy == "" {
			peer.FECPolicy = DefaultFECPolicy
		}
		switch peer.FECPolicy {
		case "latency", "balanced", "throughput":
		default:
			return nil, fmt.Errorf("Peer %d has unsupported FEC policy %q", index+1, peer.FECPolicy)
		}

		seenPeerPrefixes := make(map[netip.Prefix]struct{}, len(peer.AllowedIPs))
		for prefixIndex, prefix := range peer.AllowedIPs {
			if !prefix.IsValid() {
				return nil, fmt.Errorf("Peer %d AllowedIP %d is invalid", index+1, prefixIndex+1)
			}
			prefix = prefix.Masked()
			peer.AllowedIPs[prefixIndex] = prefix
			if _, exists := seenPeerPrefixes[prefix]; exists {
				return nil, fmt.Errorf("Peer %d AllowedIP %s is duplicated", index+1, prefix)
			}
			seenPeerPrefixes[prefix] = struct{}{}
			if owner, exists := prefixOwners[prefix]; exists {
				return nil, fmt.Errorf(
					"Peer %d AllowedIP %s is already owned by peer %s",
					index+1, prefix, shortKey(owner),
				)
			}
			prefixOwners[prefix] = publicKey
		}
		sortPrefixes(peer.AllowedIPs)
	}

	slices.SortStableFunc(cfg.Peers, func(left, right config.Peer) int {
		leftKey := peerKeys[left.PublicKey]
		rightKey := peerKeys[right.PublicKey]
		return bytes.Compare(leftKey[:], rightKey[:])
	})
	return peerKeys, nil
}

func canonicalKey(value string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("must be canonical base64: %w", err)
	}
	if len(decoded) != 32 {
		return "", fmt.Errorf("must decode to 32 bytes, got %d", len(decoded))
	}
	canonical := base64.StdEncoding.EncodeToString(decoded)
	if subtle.ConstantTimeCompare([]byte(value), []byte(canonical)) != 1 {
		return "", errors.New("must use canonical padded base64")
	}
	return canonical, nil
}

func canonicalPublicKey(value string) (string, [32]byte, error) {
	canonical, err := canonicalKey(value)
	if err != nil {
		return "", [32]byte{}, err
	}
	decoded, _ := base64.StdEncoding.DecodeString(canonical)
	var key [32]byte
	copy(key[:], decoded)
	return canonical, key, nil
}

func canonicalEndpoint(value string) (string, error) {
	spec, err := peerendpoint.Parse(value)
	if err != nil {
		return "", err
	}
	if endpoint, numeric := spec.AddrPort(); numeric {
		if endpoint.Addr().IsUnspecified() {
			return "", errors.New("address must not be unspecified")
		}
		return endpoint.String(), nil
	}
	host, err := canonicalHostname(spec.Host)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(spec.Port))), nil
}

func canonicalHostname(value string) (string, error) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" || len(value) > 253 {
		return "", errors.New("hostname length must be between 1 and 253 bytes")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 {
			return "", fmt.Errorf("invalid hostname label %q", label)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("hostname label %q starts or ends with a hyphen", label)
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') ||
				(character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return "", fmt.Errorf("hostname label %q contains an invalid character", label)
		}
	}
	return value, nil
}

func sortPrefixes(prefixes []netip.Prefix) {
	slices.SortFunc(prefixes, func(left, right netip.Prefix) int {
		if left.Addr().Is4() != right.Addr().Is4() {
			if left.Addr().Is4() {
				return -1
			}
			return 1
		}
		if left.Bits() != right.Bits() {
			return left.Bits() - right.Bits()
		}
		return left.Addr().Compare(right.Addr())
	})
}

type nonSecretProjection struct {
	Interface nonSecretInterface `json:"interface"`
	Transport config.Transport   `json:"transport"`
	Peers     []nonSecretPeer    `json:"peers"`
}

type nonSecretInterface struct {
	Addresses  []netip.Prefix `json:"addresses"`
	ListenPort uint16         `json:"listen_port"`
	FwMark     uint32         `json:"fwmark"`
	DNS        []string       `json:"dns"`
	MTU        int            `json:"mtu"`
	Table      string         `json:"table"`
	PreUp      []string       `json:"pre_up"`
	PostUp     []string       `json:"post_up"`
	PreDown    []string       `json:"pre_down"`
	PostDown   []string       `json:"post_down"`
	SaveConfig bool           `json:"save_config"`
}

type nonSecretPeer struct {
	PublicKey           string         `json:"public_key"`
	AllowedIPs          []netip.Prefix `json:"allowed_ips"`
	Endpoint            string         `json:"endpoint"`
	PersistentKeepalive uint16         `json:"persistent_keepalive"`
	FECPolicy           string         `json:"fec_policy"`
}

func digestNonSecret(cfg *config.Config) (string, error) {
	projection := nonSecretProjection{
		Interface: nonSecretInterface{
			Addresses:  append([]netip.Prefix(nil), cfg.Interface.Addresses...),
			ListenPort: cfg.Interface.ListenPort,
			FwMark:     cfg.Interface.FwMark,
			DNS:        append([]string(nil), cfg.Interface.DNS...),
			MTU:        cfg.Interface.MTU,
			Table:      cfg.Interface.Table,
			PreUp:      append([]string(nil), cfg.Interface.PreUp...),
			PostUp:     append([]string(nil), cfg.Interface.PostUp...),
			PreDown:    append([]string(nil), cfg.Interface.PreDown...),
			PostDown:   append([]string(nil), cfg.Interface.PostDown...),
			SaveConfig: cfg.Interface.SaveConfig,
		},
		Transport: cfg.Transport,
		Peers:     make([]nonSecretPeer, 0, len(cfg.Peers)),
	}
	for _, peer := range cfg.Peers {
		projection.Peers = append(projection.Peers, nonSecretPeer{
			PublicKey:           peer.PublicKey,
			AllowedIPs:          append([]netip.Prefix(nil), peer.AllowedIPs...),
			Endpoint:            peer.Endpoint,
			PersistentKeepalive: peer.PersistentKeepalive,
			FECPolicy:           peer.FECPolicy,
		})
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("encode non-secret desired projection: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func shortKey(publicKey string) string {
	if len(publicKey) <= 8 {
		return publicKey
	}
	return publicKey[:8]
}

func equalSecret(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
