// Package peerendpoint is the single parser and canonicalizer for configured
// and runtime WireGuard peer endpoints.
package peerendpoint

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

type Spec struct {
	Host    string
	Port    uint16
	Address netip.Addr
}

func (s Spec) Dynamic() bool {
	return !s.Address.IsValid()
}

func (s Spec) AddrPort() (netip.AddrPort, bool) {
	if s.Dynamic() {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(s.Address, s.Port), true
}

func Parse(value string) (Spec, error) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return Spec{}, err
	}
	if host == "" {
		return Spec{}, errors.New("host must not be empty")
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return Spec{}, errors.New("host must not contain whitespace")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return Spec{}, errors.New("port must be between 1 and 65535")
	}
	spec := Spec{Host: host, Port: uint16(port)}
	if address, err := netip.ParseAddr(host); err == nil {
		spec.Address = address.Unmap()
		spec.Host = spec.Address.String()
	}
	return spec, nil
}

func ParseNumeric(value string) (netip.AddrPort, error) {
	spec, err := Parse(value)
	if err != nil {
		return netip.AddrPort{}, err
	}
	endpoint, ok := spec.AddrPort()
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("endpoint host %q is not a numeric IP address", spec.Host)
	}
	return endpoint, nil
}

func Canonical(endpoint netip.AddrPort) (netip.AddrPort, error) {
	if !endpoint.IsValid() {
		return netip.AddrPort{}, errors.New("endpoint address is invalid")
	}
	if endpoint.Port() == 0 {
		return netip.AddrPort{}, errors.New("endpoint port must be between 1 and 65535")
	}
	return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port()), nil
}
