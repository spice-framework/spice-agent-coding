//go:build linux

package daemonprocess

import "testing"

func TestLinuxProcessStatParserHandlesParentheses(t *testing.T) {
	record, ok := (processSnapshotSource{}).parseLinuxStat(42, "42 (name with ) parens) S 7 9 9 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 1234")
	if !ok || record.pid != 42 || record.ppid != 7 || record.pgid != 9 || record.identity.startedPart != 1234 {
		t.Fatalf("parsed record = %+v, valid=%t", record, ok)
	}
}
