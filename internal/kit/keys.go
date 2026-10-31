package kit

import (
	"crypto/ed25519"
	"encoding/base64"
)

// pinnedKeys are the Ed25519 public keys whose signature makes a kit
// executable (signature.go), in standard base64. The private keys never
// touch the platform: GIIS keeps them offline and signs the kits on a machine
// of its own (docs/contrat.md, section 1).
//
// Rotation: a new key is added here, shipped in a version of the CLI, and
// signs kits only once that version is the minimum version accepted by the
// platform; a key is removed the same way. The second key is the emergency
// key, kept offline and unused while the first one is safe.
var pinnedKeys = pinned(
// sagnac-kits-1
// sagnac-kits-2 (emergency)
)

// pinned decodes the pinned keys; a malformed key is a programming error.
func pinned(keys ...string) []PublicKey {
	out := make([]PublicKey, 0, len(keys))
	for _, k := range keys {
		raw, err := base64.StdEncoding.Strict().DecodeString(k)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			panic("kit: malformed pinned key " + k)
		}
		out = append(out, PublicKey{ID: KeyID(raw), Key: ed25519.PublicKey(raw)})
	}
	return out
}
