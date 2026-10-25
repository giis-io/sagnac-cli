package oracle

import (
	"os"
	"path/filepath"
)

// DeclaredCluster is the `cluster` key of the meta.yaml in dir, "" when the
// lab declares none. It reads that key only and checks nothing else of the
// kit: "sagnac clean" must work on a kit that check would refuse.
func DeclaredCluster(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "meta.yaml"))
	if err != nil {
		return "", &UsageError{Reason: "no meta.yaml, not a lab", Path: dir}
	}
	doc, err := parseYAML(string(raw))
	if err != nil {
		return "", kitErr("meta.yaml", err.Error())
	}
	meta, ok := doc.(map[string]any)
	if !ok {
		return "", kitErr("meta.yaml", "mapping expected")
	}
	v, present := meta["cluster"]
	if !present {
		return "", nil
	}
	name, ok := v.(string)
	if !ok {
		return "", kitErr("meta.yaml", "cluster is a name")
	}
	return name, nil
}
