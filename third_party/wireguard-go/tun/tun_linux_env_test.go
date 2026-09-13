//go:build linux

package tun

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestTunOffloadDisabledEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset"},
		{name: "false", value: "false"},
		{name: "true", value: "true", want: true},
		{name: "one", value: "1", want: true},
		{name: "invalid", value: "sometimes", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(disableTUNOffloadEnvironment, test.value)
			got, err := tunOffloadDisabled()
			if (err != nil) != test.wantErr {
				t.Fatalf("tunOffloadDisabled() error = %v, wantErr %t", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("tunOffloadDisabled() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestNativeTunWriteWithoutGSOOffloadUsesZeroVirtioHeader(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	payload := []byte{0x45, 0, 0, 3}
	buffer := make([]byte, virtioNetHdrLen+len(payload))
	copy(buffer[virtioNetHdrLen:], payload)
	for i := range buffer[:virtioNetHdrLen] {
		buffer[i] = 0xff
	}
	tun := &NativeTun{
		tunFile:     writer,
		vnetHdr:     true,
		gsoOffload:  false,
		toWrite:     make([]int, 0, 1),
		tcpGROTable: newTCPGROTable(),
		udpGROTable: newUDPGROTable(),
	}

	written, err := tun.Write([][]byte{buffer}, virtioNetHdrLen)
	if err != nil {
		t.Fatal(err)
	}
	if written != len(buffer) {
		t.Fatalf("Write() = %d bytes, want %d", written, len(buffer))
	}
	got := make([]byte, len(buffer))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:virtioNetHdrLen], make([]byte, virtioNetHdrLen)) {
		t.Fatalf("virtio header = %x, want all zeroes", got[:virtioNetHdrLen])
	}
	if !bytes.Equal(got[virtioNetHdrLen:], payload) {
		t.Fatalf("payload = %x, want %x", got[virtioNetHdrLen:], payload)
	}
}
