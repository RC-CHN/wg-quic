package quick

import (
	"errors"
	"os"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/control"
)

func TestDesktopStatusPreservesUnavailableAndInactiveDistinction(t *testing.T) {
	for _, test := range []struct {
		name, state, code string
		err               error
	}{
		{"missing", "inactive", "", os.ErrNotExist},
		{"denied", "unknown", "permission_denied", os.ErrPermission},
		{"timeout", "unknown", "timeout", os.ErrDeadlineExceeded},
		{"malformed", "unknown", "status_unavailable", errors.New("bad JSON")},
		{"mixed-errors", "unknown", "permission_denied", errors.Join(os.ErrNotExist, os.ErrPermission)},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := desktopStatusResult("wg0", control.Status{}, test.err)
			if result.State != test.state {
				t.Fatalf("state = %s", result.State)
			}
			if test.code != "" && (result.Failure == nil || result.Failure.Code != test.code) {
				t.Fatalf("error = %+v", result.Failure)
			}
		})
	}
	for _, state := range []string{"up", "prepared", "down"} {
		result := desktopStatusResult("wg0", control.Status{Interface: "wg0", State: state}, nil)
		if result.Status == nil || result.Failure != nil {
			t.Fatalf("lost valid state: %+v", result)
		}
	}
	if got := desktopStatusResult("wg0", control.Status{Interface: "other", State: "up"}, nil); got.State != "unknown" {
		t.Fatal("accepted status from another interface")
	}
}
