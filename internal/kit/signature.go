package kit

import (
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
)

// A kit runs code on the machine of the student (make setup, checks/,
// clean). Its SHA-256 is announced by the platform, which only proves that the
// bytes were not changed on the way. The signature proves that the kit was
// built and signed by GIIS outside the platform: a compromised platform can
// refuse to serve a kit, not make the CLI run one of its own.
//
// The signed message is fixed and versioned:
//
//	sagnac-kit-signature-v1
//	lab=<lab>
//	version=<version>
//	tar_sha256=<SHA-256 of the uncompressed tar, lowercase hex>
//
// each line ended by "\n". The digest is taken on the tar stream, not on the
// gzip file, so that the signer can rebuild the kit on its own machine and
// sign it without depending on the bytes of a given zlib. The lab and the
// version are signed with it: a signed kit cannot be served for another lab
// or under another version number.
//
// The signature is Ed25519 (RFC 8032), sent by the platform in two headers of
// the download: X-Sagnac-Kit-Key-Id (the identifier of the key, see KeyID)
// and X-Sagnac-Kit-Signature (the 64 bytes, standard base64).

const (
	codeSignature      = "KIT_SIGNATURE_INVALID"
	headerKeyID        = "X-Sagnac-Kit-Key-Id"
	headerSignature    = "X-Sagnac-Kit-Signature"
	signatureMagicLine = "sagnac-kit-signature-v1"
)

// ErrSignature: the kit carries no signature, or none made by a key pinned
// in this binary, or the signature does not match the lab, the version or
// the content received. Nothing is written.
var ErrSignature = errors.New(codeSignature)

// trustedKeys are the keys a signature is checked against: the pinned keys,
// which only tests replace (SetTrustedKeys).
var trustedKeys = pinnedKeys

// PublicKey is a kit signing key pinned in the binary.
type PublicKey struct {
	ID  string
	Key ed25519.PublicKey
}

// KeyID is the identifier of an Ed25519 public key: the first 8 bytes of its
// SHA-256, in lowercase hex (16 characters).
func KeyID(key ed25519.PublicKey) string {
	h := sha256.Sum256(key)
	return hex.EncodeToString(h[:8])
}

// SignedMessage is the message signed for a kit (see above).
func SignedMessage(lab string, version int, tarSHA256 string) []byte {
	return []byte(signatureMagicLine + "\nlab=" + lab + "\nversion=" + strconv.Itoa(version) +
		"\ntar_sha256=" + tarSHA256 + "\n")
}

// TarDigest returns the SHA-256 of the uncompressed content of a gzip
// archive, reading at most limit uncompressed bytes.
func TarDigest(archive io.Reader, limit int64) (string, error) {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(gz, limit+1))
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", &UnsafeError{Reason: reasonTooLarge}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// maxTar bounds the uncompressed stream read for the digest: the extracted
// files (MaxTotal) plus the tar headers of MaxEntries entries, generously.
func maxTar() int64 { return MaxTotal + int64(MaxEntries)*4096 }

// SetTrustedKeys replaces the pinned keys and returns a function that puts
// them back. For tests only: the binary never calls it.
func SetTrustedKeys(keys []PublicKey) (restore func()) {
	old := trustedKeys
	trustedKeys = keys
	return func() { trustedKeys = old }
}

// verifySignature checks a signature received in the headers of a download
// against the keys pinned in the binary.
func verifySignature(lab string, version int, tarSHA256, keyID, signature string) error {
	sig, err := base64.StdEncoding.Strict().DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrSignature
	}
	msg := SignedMessage(lab, version, tarSHA256)
	for _, k := range trustedKeys {
		if k.ID == keyID && len(k.Key) == ed25519.PublicKeySize && ed25519.Verify(k.Key, msg, sig) {
			return nil
		}
	}
	return ErrSignature
}
