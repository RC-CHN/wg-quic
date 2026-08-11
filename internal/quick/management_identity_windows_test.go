//go:build windows

package quick

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsManagementPipeSecurityDescriptor(t *testing.T) {
	descriptor, err := windowsManagementPipeSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	if !descriptor.IsValid() {
		t.Fatal("management pipe security descriptor is invalid")
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if !owner.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatalf("management pipe owner = %v, want LocalSystem", owner)
	}

	rendered := descriptor.String()
	lowerRendered := strings.ToLower(rendered)
	for _, want := range []string{
		"O:SY",
		"(A;;GA;;;SY)",
		"(A;;GA;;;BA)",
		"0x12019b;;;AU)",
		"(ML;;NW;;;ME)",
	} {
		if !strings.Contains(lowerRendered, strings.ToLower(want)) {
			t.Errorf("security descriptor %q does not contain %q", rendered, want)
		}
	}
	if strings.Contains(lowerRendered, "gw;;;au)") {
		t.Fatalf(
			"management pipe grants GenericWrite/create-instance to AU: %q",
			rendered,
		)
	}
}

func TestWindowsManagementPipeDialConfig(t *testing.T) {
	config, err := windowsManagementPipeDialConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.ImpersonationLevel != windows.SECURITY_IDENTIFICATION {
		t.Fatalf(
			"impersonation level = %#x, want SECURITY_IDENTIFICATION",
			config.ImpersonationLevel,
		)
	}
	if config.DesiredAccess != windowsManagementClientAccess {
		t.Fatalf(
			"desired access = %#x, want %#x",
			config.DesiredAccess, windowsManagementClientAccess,
		)
	}
	const fileCreatePipeInstance = uint32(0x00000004)
	if config.DesiredAccess&fileCreatePipeInstance != 0 {
		t.Fatalf(
			"desired access %#x includes FILE_CREATE_PIPE_INSTANCE",
			config.DesiredAccess,
		)
	}
	if config.ExpectedOwner == nil ||
		!config.ExpectedOwner.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatalf("expected owner = %v, want LocalSystem", config.ExpectedOwner)
	}
	if !strings.HasPrefix(
		strings.ToLower(windowsManagementPipePath), `\\.\pipe\`,
	) {
		t.Fatalf("management pipe path %q is not local", windowsManagementPipePath)
	}
}

func TestAuthorizeWindowsManagementIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity windowsManagementIdentity
		allowed  bool
	}{
		{
			name: "LocalSystem",
			identity: windowsManagementIdentity{
				localSystem: true,
			},
			allowed: true,
		},
		{
			name: "full Administrator",
			identity: windowsManagementIdentity{
				administrator: true,
			},
			allowed: true,
		},
		{
			name: "UAC-filtered Administrator",
			identity: windowsManagementIdentity{
				linkedAdministrator: true,
			},
			allowed: true,
		},
		{
			name: "synthetic LUA Administrator with exact deny-only SID",
			identity: windowsManagementIdentity{
				limitedAdministratorDenyOnlyGroup: true,
			},
			allowed: true,
		},
		{
			name: "limited user with ordinarily disabled Administrator SID",
			identity: windowsManagementIdentity{
				limitedAdministratorDenyOnlyGroup: false,
			},
			allowed: false,
		},
		{
			name:     "ordinary authenticated user",
			identity: windowsManagementIdentity{},
			allowed:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := authorizeWindowsManagementIdentity(test.identity)
			if test.allowed && err != nil {
				t.Fatalf("authorization failed: %v", err)
			}
			if !test.allowed && !errors.Is(
				err, errWindowsManagementUnauthorized,
			) {
				t.Fatalf(
					"authorization error = %v, want unauthorized sentinel",
					err,
				)
			}
		})
	}
}

func TestClassifyWindowsTokenGroupAttributes(t *testing.T) {
	tests := []struct {
		name          string
		attributes    uint32
		enabled       bool
		exactDenyOnly bool
	}{
		{
			name:       "enabled Administrator",
			attributes: windows.SE_GROUP_ENABLED,
			enabled:    true,
		},
		{
			name:          "exact deny-only Administrator",
			attributes:    windows.SE_GROUP_USE_FOR_DENY_ONLY,
			exactDenyOnly: true,
		},
		{
			name:       "ordinary disabled Administrator",
			attributes: 0,
		},
		{
			name: "deny-only with an extra attribute is rejected",
			attributes: windows.SE_GROUP_USE_FOR_DENY_ONLY |
				windows.SE_GROUP_MANDATORY,
		},
		{
			name: "inconsistent enabled and deny-only is rejected",
			attributes: windows.SE_GROUP_ENABLED |
				windows.SE_GROUP_USE_FOR_DENY_ONLY,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			membership := classifyWindowsTokenGroupAttributes(test.attributes)
			if membership.enabled != test.enabled ||
				membership.exactDenyOnly != test.exactDenyOnly {
				t.Fatalf(
					"membership = %+v, want enabled=%v exactDenyOnly=%v",
					membership, test.enabled, test.exactDenyOnly,
				)
			}
		})
	}
}
