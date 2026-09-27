package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestDesktopEditedConfigurationRetainsGoSemantics(t *testing.T) {
	root := filepath.Join("..", "..", "tests", "fixtures", "desktop")
	before, err := ParseFile(filepath.Join(root, "peers-before.conf"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseFile(filepath.Join(root, "peers-after.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Peers) != 2 || after.Peers[0].Endpoint != "updated.example:8443" {
		t.Fatal("fixture edit did not reach the intended peer")
	}
	before.Peers[0].Endpoint = after.Peers[0].Endpoint
	if !reflect.DeepEqual(before, after) {
		t.Fatal("form edit changed other Go configuration semantics")
	}
}
