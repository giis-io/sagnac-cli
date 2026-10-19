package i18n

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}

var both = []string{"fr", "en"}

func TestLanguageChoice(t *testing.T) {
	cases := []struct {
		name string
		flag string
		env  map[string]string
		want string
	}{
		{"default is fr", "", nil, "fr"},
		{"flag beats LANG", "en", map[string]string{"LANG": "fr_FR.UTF-8"}, "en"},
		{"flag beats LC_ALL", "fr", map[string]string{"LC_ALL": "en_US.UTF-8"}, "fr"},
		{"LANG alone", "", map[string]string{"LANG": "en_US.UTF-8"}, "en"},
		{"LC_ALL beats LC_MESSAGES and LANG", "", map[string]string{"LC_ALL": "en_US", "LC_MESSAGES": "fr_FR", "LANG": "fr_FR"}, "en"},
		{"LC_MESSAGES beats LANG", "", map[string]string{"LC_MESSAGES": "en_GB", "LANG": "fr_FR"}, "en"},
		{"unknown locale falls back to fr", "", map[string]string{"LANG": "de_DE.UTF-8"}, "fr"},
		{"unknown flag falls back to fr", "de", map[string]string{"LANG": "en_US"}, "fr"},
		{"C locale falls back to fr", "", map[string]string{"LANG": "C"}, "fr"},
		{"case and modifier ignored", "", map[string]string{"LANG": "EN_us@euro"}, "en"},
	}
	for _, c := range cases {
		if got := Choose(c.flag, env(c.env), both); got != c.want {
			t.Errorf("%s: Choose = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOnlyFrenchEmbedded(t *testing.T) {
	if got := Select("en", env(nil)); got != "fr" {
		t.Errorf("Select(en) = %q, want fr while only fr.json is embedded", got)
	}
}

func TestTranslate(t *testing.T) {
	if err := Use("fr"); err != nil {
		t.Fatal(err)
	}
	if got := T("version.line", "9.9.9"); !strings.Contains(got, "9.9.9") {
		t.Errorf("T did not format its arguments: %q", got)
	}
	if got := T("no.such.key"); got != "no.such.key" {
		t.Errorf("missing key should return the key, got %q", got)
	}
}

func TestCatalogueWellFormed(t *testing.T) {
	keys := Keys()
	if len(keys) == 0 {
		t.Fatal("empty catalogue")
	}
	for _, k := range keys {
		if strings.TrimSpace(T(k)) == "" {
			t.Errorf("key %q has an empty message", k)
		}
	}
}
