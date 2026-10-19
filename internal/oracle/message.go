package oracle

import (
	"os"
	"path/filepath"
	"strings"
)

// MessageText returns the text of the message of code, without its final
// blank (docs/contrat.md, section 6): checks/messages/<lang>/ of the
// lab, then its French one, then the same in the messages of the cadre. The
// placeholders {observed} and {expected} are left to the caller. The second
// value is false when no file exists; the code is then shown.
func (l *Lab) MessageText(code, lang string) (string, bool) {
	if !reCode.MatchString(code) {
		return "", false
	}
	cadre := filepath.Dir(filepath.Dir(l.libPath))
	for _, base := range []string{filepath.Join(l.Dir, "checks", "messages"), filepath.Join(cadre, "messages")} {
		for _, lg := range []string{lang, "fr"} {
			if lg == "" || strings.ContainsAny(lg, `/\.`) {
				continue
			}
			p := filepath.Join(base, lg, code+".txt")
			if !isRegular(p) {
				continue
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			return pyStrip(strings.ToValidUTF8(string(raw), "\uFFFD")), true
		}
	}
	return "", false
}
