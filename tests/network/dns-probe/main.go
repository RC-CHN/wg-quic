// dns-probe is a test-only authoritative responder for checking Windows DNS
// policy through the tunnel. It does not forward queries or act as a resolver.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:5353", "UDP listen address on the isolated test peer")
	flag.Parse()
	if err := run(*listen); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(address string) error {
	conn, err := net.ListenPacket("udp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	log := json.NewEncoder(os.Stdout)
	buffer := make([]byte, 4096)
	for {
		n, remote, err := conn.ReadFrom(buffer)
		if err != nil {
			return err
		}
		var request dnsmessage.Message
		if err := request.Unpack(buffer[:n]); err != nil || request.Header.Response || len(request.Questions) != 1 {
			continue
		}
		question := request.Questions[0]
		name := strings.ToLower(question.Name.String())
		response := dnsmessage.Message{
			Header: dnsmessage.Header{
				ID: request.ID, Response: true, Authoritative: true,
				RecursionDesired: request.RecursionDesired,
			},
			Questions: request.Questions,
		}
		matched := question.Class == dnsmessage.ClassINET && strings.HasSuffix(name, ".wgq-native.test.invalid.")
		if !matched {
			response.RCode = dnsmessage.RCodeNameError
		} else if question.Type == dnsmessage.TypeA {
			response.Answers = []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 0},
				Body:   &dnsmessage.AResource{A: netip.MustParseAddr("203.0.113.73").As4()},
			}}
		}
		packet, err := response.Pack()
		if err != nil {
			return err
		}
		if _, err := conn.WriteTo(packet, remote); err != nil {
			return err
		}
		if err := log.Encode(map[string]any{
			"utc": time.Now().UTC(), "remote": remote.String(), "name": name,
			"type": uint16(question.Type), "matched": matched,
			"answers": len(response.Answers), "rcode": uint16(response.RCode),
		}); err != nil {
			return err
		}
	}
}
