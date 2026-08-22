//go:build windows

package management

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestManagementPipeSamePrivilegedProcessConnects(t *testing.T) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(
		windows.WinBuiltinAdministratorsSid,
	)
	if err != nil {
		t.Fatal(err)
	}
	elevated, err := token.IsMember(administrators)
	if err != nil {
		t.Fatal(err)
	}
	if !elevated && !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Skip("management mutation transport requires LocalSystem or an elevated Administrator")
	}

	path := fmt.Sprintf(
		`\\.\pipe\wg-quic-management-transport-test-%d-%d`,
		os.Getpid(), time.Now().UnixNano(),
	)
	listener, _, err := listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct {
		connection net.Conn
		err        error
	}, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		accepted <- struct {
			connection net.Conn
			err        error
		}{connection: connection, err: acceptErr}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case result := <-accepted:
		if result.err != nil {
			t.Fatal(result.err)
		}
		result.connection.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestManagementPipeSecurityOwnerMatchesCreator(t *testing.T) {
	descriptor, err := managementPipeSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if !owner.Equals(user.User.Sid) {
		t.Fatalf("management pipe owner = %v, want creator %v", owner, user.User.Sid)
	}
}

func TestManagementPipeExpectedOwnersAreNarrow(t *testing.T) {
	owners, err := managementPipeExpectedOwners()
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) == 0 || !owners[0].IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatalf("management expected owners = %v, want LocalSystem first", owners)
	}
	if len(owners) > 2 {
		t.Fatalf("management expected owners are too broad: %v", owners)
	}
	if len(owners) == 2 {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		if !owners[1].Equals(user.User.Sid) {
			t.Fatalf("second management owner = %v, want current elevated user %v", owners[1], user.User.Sid)
		}
	}
}
