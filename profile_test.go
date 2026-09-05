package transport

import "testing"

func TestOnlyCurrentProfileAccepted(t *testing.T) {
	for _, profile := range []string{"v1", "continuous-v1", "continuous-bulk-pipeline"} {
		handler := &Transport{ApplicationRoot: testApplicationRoot(t), Profile: profile, ForwardProxy: testForwardProxy()}
		if err := handler.Provision(testCaddyContext(t)); err == nil {
			handler.Cleanup()
			t.Fatal("retired profile accepted")
		}
	}
	if len(startupSlots) != 20 {
		t.Fatal("startup graph changed")
	}
}
