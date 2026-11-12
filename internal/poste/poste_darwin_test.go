package poste

import "testing"

func TestParseMemsize(t *testing.T) {
	for _, c := range []struct {
		out  string
		want uint64
		ok   bool
	}{
		{"17179869184\n", 17179869184, true},
		{"8589934592", 8589934592, true},
		{"0\n", 0, false},
		{"hw.memsize: 17179869184\n", 0, false},
		{"", 0, false},
	} {
		if got, ok := parseMemsize(c.out); got != c.want || ok != c.ok {
			t.Errorf("parseMemsize(%q) = %d, %v", c.out, got, ok)
		}
	}
}

func TestPlaceholderOnlyForTheStandIns(t *testing.T) {
	for _, path := range []string{"/opt/homebrew/bin/git", "/usr/bin/curl", "/usr/local/bin/python3"} {
		if placeholder(path) {
			t.Errorf("%s taken for a stand-in", path)
		}
	}
}
