package quick

import "testing"

func TestWindowsDiagnosticRequestsHaveFixedScope(t *testing.T) {
	valid := windowsManagementRequest{Action: "collect", Name: "wg0", PeerPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	if err := validateWindowsManagementRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*windowsManagementRequest){
		func(r *windowsManagementRequest) { r.Action = "down" },
		func(r *windowsManagementRequest) { r.RequestID = "0123456789abcdef0123456789abcdef" },
		func(r *windowsManagementRequest) { r.Config = []byte("configuration") },
		func(r *windowsManagementRequest) { r.Overwrite = true },
		func(r *windowsManagementRequest) { r.PeerPublicKey = "short-key" },
	} {
		request := valid
		mutate(&request)
		if err := validateWindowsManagementRequest(request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
}
