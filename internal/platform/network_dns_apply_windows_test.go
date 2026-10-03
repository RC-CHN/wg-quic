//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDNSApplySelectsOnlyRequestedFields(t *testing.T) {
	type call struct {
		flags           uint64
		servers, domain string
	}
	for _, test := range []struct {
		name   string
		values []string
		want   []call
	}{
		{"mixed_order", []string{"2001:db8::54", "192.0.2.54", "2001:db8::53", "192.0.2.53", "corp.example"}, []call{{2, "192.0.2.54,192.0.2.53", ""}, {3, "2001:db8::54,2001:db8::53", ""}, {32, "", "corp.example"}}},
		{"v4_only", []string{"192.0.2.53"}, []call{{2, "192.0.2.53", ""}}},
		{"v6_only", []string{"2001:db8::53"}, []call{{3, "2001:db8::53", ""}}},
		{"suffix_only", []string{"corp.example"}, []call{{32, "", "corp.example"}}},
		{"none", nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []call
			applied, err := applyWindowsDNSSettings(t.Context(), test.values, func(s *windowsDNSInterfaceSettings) error {
				got = append(got, call{s.Flags, windows.UTF16PtrToString(s.NameServer), windows.UTF16PtrToString(s.Domain)})
				if s.Version != 1 || s.SearchList != nil || s.ProfileNameServer != nil || s.RegistrationEnabled != 0 || s.RegisterAdapterName != 0 || s.EnableLLMNR != 0 || s.QueryAdapterName != 0 {
					t.Fatalf("unselected settings populated: %#v", s)
				}
				if (s.Flags&windowsDNSSettingsNameServer != 0) != (s.NameServer != nil) || (s.Flags&windowsDNSSettingsDomain != 0) != (s.Domain != nil) {
					t.Fatalf("selected field must have an explicit value: %#v", s)
				}
				return nil
			})
			if err != nil || applied != (len(test.want) != 0) || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("applied=%v err=%v calls=%v want=%v", applied, err, got, test.want)
			}
		})
	}
}

func TestWindowsDNSApplyReportsPartialMutation(t *testing.T) {
	values := []string{"192.0.2.53", "2001:db8::53", "corp.example"}
	for failAt := 1; failAt <= 3; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			calls := 0
			applied, err := applyWindowsDNSSettings(t.Context(), values, func(*windowsDNSInterfaceSettings) error {
				calls++
				if calls == failAt {
					return windows.ERROR_ACCESS_DENIED
				}
				return nil
			})
			if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || applied != (failAt > 1) || calls != failAt {
				t.Fatalf("applied=%v calls=%d err=%v", applied, calls, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	applied, err := applyWindowsDNSSettings(ctx, values, func(*windowsDNSInterfaceSettings) error { calls++; cancel(); return nil })
	if !applied || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("partial cancellation: applied=%v calls=%d err=%v", applied, calls, err)
	}
	applied, err = (windowsNativeNetworkSystem{}).ApplyDNS(ctx, 0, 0, values)
	if applied || !errors.Is(err, context.Canceled) {
		t.Fatalf("native canceled entry: applied=%v err=%v", applied, err)
	}
	for _, invalid := range [][]string{{"192.0.2.53", "bad\x00suffix"}, {"one.example", "two.example"}} {
		calls = 0
		applied, err = applyWindowsDNSSettings(t.Context(), invalid, func(*windowsDNSInterfaceSettings) error { calls++; return nil })
		if applied || err == nil || calls != 0 {
			t.Fatalf("invalid settings mutated: applied=%v calls=%d err=%v", applied, calls, err)
		}
	}
}

func TestWindowsDNSApplyOwnershipAndFallback(t *testing.T) {
	for _, test := range []struct {
		name    string
		applied bool
		err     error
		want    []string
	}{
		{"native", false, nil, []string{"apply-dns", "dns"}},
		{"missing", false, errWindowsDNSAPIUnavailable, []string{"apply-dns", "apply", "dns"}},
		{"first_failure", false, windows.ERROR_ACCESS_DENIED, []string{"apply-dns"}},
		{"partial_failure", true, windows.ERROR_ACCESS_DENIED, []string{"apply-dns", "dns"}},
		{"partial_cancellation", true, context.Canceled, []string{"apply-dns", "dns"}},
		{"unsupported", false, windows.ERROR_NOT_SUPPORTED, []string{"apply-dns"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			system := &fakeWindowsNetworkSystem{dnsApplied: test.applied, completed: []int{0}, failures: map[string]error{"apply-dns": test.err}}
			state := &windowsNetworkState{name: "wg0", interfaceLUID: 77, compartmentID: 9}
			operation, err := windowsDNSOperation(windowsPowerShellBase("wg0"), []string{"192.0.2.53"})
			if err != nil {
				t.Fatal(err)
			}
			err = state.apply(t.Context(), []windowsOperation{operation}, system)
			if test.err != nil && test.name != "missing" {
				if !errors.Is(err, test.err) {
					t.Fatalf("lost apply error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := state.rollback(t.Context(), system); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(system.calls, test.want) {
				t.Fatalf("calls=%v want=%v", system.calls, test.want)
			}
			for i, luid := range system.luids {
				if luid != 77 {
					t.Fatalf("call%d wrong LUID %d", i, luid)
				}
			}
			for _, compartment := range system.compartments {
				if compartment != 9 {
					t.Fatalf("wrong compartment %d", compartment)
				}
			}
		})
	}
}
