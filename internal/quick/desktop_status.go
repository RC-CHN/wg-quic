package quick

import (
	"errors"
	"net"
	"os"
	"time"

	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platformenv"
)

// DesktopStatus is a versioned unprivileged projection. Expected unavailable
// states are data, so shells never have to interpret translated OS messages.
type DesktopStatus struct {
	ProtocolVersion int                   `json:"protocol_version"`
	State           string                `json:"state"`
	SampledAt       int64                 `json:"sampled_at"`
	Status          *control.Status       `json:"status,omitempty"`
	Failure         *DesktopStatusFailure `json:"error,omitempty"`
}

type DesktopStatusFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func ReadDesktopStatus(name string) (DesktopStatus, error) {
	host := platformenv.Paths{}
	if err := host.ValidateInterfaceName(name); err != nil {
		return DesktopStatus{}, err
	}
	status, err := control.ReadOnly(host.ControlPath(name))
	return desktopStatusResult(name, status, err), nil
}

func desktopStatusResult(name string, status control.Status, err error) DesktopStatus {
	result := DesktopStatus{ProtocolVersion: 1, State: "unknown", SampledAt: time.Now().UnixMilli()}
	if err == nil {
		if status.Interface != name || (status.State != "up" && status.State != "prepared" && status.State != "down") {
			err = errors.New("status response has an unexpected interface or state")
		} else {
			result.State = status.State
			if status.State == "down" {
				result.State = "inactive"
			}
			result.Status = &status
			return result
		}
	}
	code := "status_unavailable"
	var networkError net.Error
	switch {
	case errors.Is(err, os.ErrPermission):
		code = "permission_denied"
	case errors.Is(err, os.ErrNotExist):
		result.State = "inactive"
		return result
	case errors.As(err, &networkError) && networkError.Timeout():
		code = "timeout"
	}
	result.Failure = &DesktopStatusFailure{Code: code, Message: err.Error()}
	return result
}
