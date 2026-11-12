package poste

import "testing"

func TestParseMeminfo(t *testing.T) {
	for _, c := range []struct {
		text string
		want uint64
		ok   bool
	}{
		{"MemTotal:       16308920 kB\nMemFree:  1 kB\n", 16308920 << 10, true},
		{"MemFree: 1 kB\nMemTotal: 8000000 kB\n", 8000000 << 10, true},
		{"MemTotal: 0 kB\n", 0, false},
		{"MemTotal: beaucoup kB\n", 0, false},
		{"MemTotal: 16308920\n", 0, false},
		{"", 0, false},
	} {
		if got, ok := parseMeminfo(c.text); got != c.want || ok != c.ok {
			t.Errorf("parseMeminfo(%q) = %d, %v", c.text, got, ok)
		}
	}
}

func TestWSLRelease(t *testing.T) {
	for release, want := range map[string]bool{
		"5.15.167.4-microsoft-standard-WSL2\n": true,
		"4.4.0-19041-Microsoft\n":              true,
		"6.8.0-48-generic\n":                   false,
		"":                                     false,
	} {
		if got := wslRelease(release); got != want {
			t.Errorf("wslRelease(%q) = %v", release, got)
		}
	}
}
