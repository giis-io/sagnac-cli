// Package i18n provides the message catalogue of the CLI. Every text shown to
// a person goes through T; keys are in English, messages are in the language
// of the catalogue (French is the reference language).
package i18n

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed fr.json
var frJSON []byte

const fallback = "fr"

var (
	mu        sync.RWMutex
	catalogs  = map[string]map[string]string{}
	current   = fallback
	loadError error
)

func init() {
	messages := map[string]string{}
	if err := json.Unmarshal(frJSON, &messages); err != nil {
		loadError = err
		return
	}
	catalogs[fallback] = messages
}

// Available returns the languages embedded in the binary, sorted.
func Available() []string {
	mu.RLock()
	defer mu.RUnlock()
	langs := make([]string, 0, len(catalogs))
	for l := range catalogs {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return langs
}

// Choose picks the language: the flag first, then the first non-empty of
// LC_ALL, LC_MESSAGES and LANG, then French. A language that is not in
// available falls back to French.
func Choose(flag string, getenv func(string) string, available []string) string {
	candidate := flag
	if candidate == "" {
		for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
			if v := getenv(name); v != "" {
				candidate = v
				break
			}
		}
	}
	candidate = normalize(candidate)
	for _, l := range available {
		if l == candidate {
			return l
		}
	}
	return fallback
}

// Select is Choose over the languages embedded in the binary.
func Select(flag string, getenv func(string) string) string {
	return Choose(flag, getenv, Available())
}

// normalize reduces a locale such as "EN_us.UTF-8@euro" to "en".
func normalize(locale string) string {
	for _, sep := range []string{"@", ".", "_", "-"} {
		if i := strings.Index(locale, sep); i >= 0 {
			locale = locale[:i]
		}
	}
	return strings.ToLower(locale)
}

// Use sets the language of T. An unknown language is an error.
func Use(lang string) error {
	if loadError != nil {
		return loadError
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := catalogs[lang]; !ok {
		return fmt.Errorf("i18n: language %q not embedded", lang)
	}
	current = lang
	return nil
}

// Current returns the language used by T.
func Current() string {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// T returns the message for key, formatted with args. A key missing from the
// current language falls back to French, then to the key itself.
func T(key string, args ...any) string {
	mu.RLock()
	msg, ok := catalogs[current][key]
	if !ok {
		msg, ok = catalogs[fallback][key]
	}
	mu.RUnlock()
	if !ok {
		return key
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// Keys returns the keys of the reference catalogue, sorted.
func Keys() []string {
	mu.RLock()
	defer mu.RUnlock()
	keys := make([]string, 0, len(catalogs[fallback]))
	for k := range catalogs[fallback] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
