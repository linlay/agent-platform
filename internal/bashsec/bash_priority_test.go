package bashsec

import "testing"

func TestHardBlockCannotBeHiddenAfterSoftApproval(t *testing.T) {
	for _, command := range []string{"echo hi > output; eval bad", "echo hi > output < /etc/passwd", "echo hi > output; cat /proc/1/environ"} {
		if got := ReviewBashSecurity(command); got.Decision != ReviewBlock {
			t.Fatalf("%s: %+v", command, got)
		}
	}
}
