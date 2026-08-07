//go:build linux

package processcontainment

import "testing"

func TestParseLinuxProcessStatPreservesBirthIdentity(t *testing.T) {
	t.Parallel()
	record, ok := ParseLinuxProcessStat(42, "42 (name with ) parens) S 7 9 9 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 1234")
	if !ok || record.PID != 42 || record.ParentID != 7 || record.GroupID != 9 ||
		record.Identity.StartedPart != 1234 {
		t.Fatalf("parsed record = %#v, valid=%t", record, ok)
	}
	for _, value := range []string{"", "42 malformed", "42 (name) S invalid 9"} {
		if invalid, valid := ParseLinuxProcessStat(42, value); valid || invalid != (Record{}) {
			t.Fatalf("invalid stat = %#v, valid=%t", invalid, valid)
		}
	}
}
