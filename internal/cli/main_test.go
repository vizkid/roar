package cli

import "testing"

// roard ran under the two-minute command deadline, so the service manager
// restarted it every 120 s and each restart left a lost client behind.
func TestServiceCommandsHaveNoDeadline(t *testing.T) {
	for name, want := range map[string]bool{"daemon": false, "send": true, "status": true} {
		ctx, cancel := commandContext(name)
		_, has := ctx.Deadline()
		cancel()
		if has != want {
			t.Errorf("%s: deadline %v, want %v", name, has, want)
		}
		if ctx.Err() == nil {
			t.Errorf("%s: cancel did not end the context", name)
		}
	}
}
