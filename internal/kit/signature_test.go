package kit

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

// testKey signs the kits of the tests of this package; TestMain pins it.
var testKey ed25519.PrivateKey

func TestMain(m *testing.M) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	testKey = priv
	restore := SetTrustedKeys([]PublicKey{{ID: KeyID(pub), Key: pub}})
	code := m.Run()
	restore()
	os.Exit(code)
}

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// signWith returns the two headers of a kit signed by key for lab and version.
func signWith(t *testing.T, key ed25519.PrivateKey, lab string, version int, archive []byte) (string, string) {
	t.Helper()
	d, err := TarDigest(bytes.NewReader(archive), maxTar())
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(key, SignedMessage(lab, version, d))
	return KeyID(key.Public().(ed25519.PublicKey)), base64.StdEncoding.EncodeToString(sig)
}

// signedHeaders sets the digest and a valid signature of archive, for lab hello v3.
func signedHeaders(t *testing.T, w http.ResponseWriter, archive []byte) {
	t.Helper()
	id, sig := signWith(t, testKey, "hello", 3, archive)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Sagnac-Kit-Sha256", sum(archive))
	w.Header().Set(headerKeyID, id)
	w.Header().Set(headerSignature, sig)
}

func TestSignedMessageIsFixed(t *testing.T) {
	got := string(SignedMessage("hello", 3, strings.Repeat("a", 64)))
	want := "sagnac-kit-signature-v1\nlab=hello\nversion=3\ntar_sha256=" + strings.Repeat("a", 64) + "\n"
	if got != want {
		t.Errorf("message %q", got)
	}
}

// Every way of not being a kit signed by a pinned key for this lab, this
// version and this content is refused, and nothing is left in the directory.
func TestDownloadRefusesUnsignedKits(t *testing.T) {
	archive := gzipped(t, []byte("tar bytes"))
	_, stranger, _ := ed25519.GenerateKey(nil)
	cases := map[string]func(t *testing.T, w http.ResponseWriter){
		"no signature": func(t *testing.T, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("X-Sagnac-Kit-Sha256", sum(archive))
		},
		"key not pinned": func(t *testing.T, w http.ResponseWriter) {
			signedHeaders(t, w, archive)
			id, sig := signWith(t, stranger, "hello", 3, archive)
			w.Header().Set(headerKeyID, id)
			w.Header().Set(headerSignature, sig)
		},
		"pinned id, other key": func(t *testing.T, w http.ResponseWriter) {
			signedHeaders(t, w, archive)
			_, sig := signWith(t, stranger, "hello", 3, archive)
			w.Header().Set(headerSignature, sig)
		},
		"other lab": func(t *testing.T, w http.ResponseWriter) {
			signedHeaders(t, w, archive)
			_, sig := signWith(t, testKey, "hello2", 3, archive)
			w.Header().Set(headerSignature, sig)
		},
		"other version": func(t *testing.T, w http.ResponseWriter) {
			signedHeaders(t, w, archive)
			_, sig := signWith(t, testKey, "hello", 2, archive)
			w.Header().Set(headerSignature, sig)
		},
		"other content": func(t *testing.T, w http.ResponseWriter) {
			signedHeaders(t, w, archive)
			_, sig := signWith(t, testKey, "hello", 3, gzipped(t, []byte("other tar")))
			w.Header().Set(headerSignature, sig)
		},
		"not base64": func(t *testing.T, w http.ResponseWriter) {
			signedHeaders(t, w, archive)
			w.Header().Set(headerSignature, "%%%")
		},
		"truncated": func(t *testing.T, w http.ResponseWriter) {
			signedHeaders(t, w, archive)
			_, sig := signWith(t, testKey, "hello", 3, archive)
			w.Header().Set(headerSignature, sig[:40])
		},
	}
	for name, headers := range cases {
		s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			headers(t, w)
			w.Write(archive)
		})
		work := t.TempDir()
		f, err := Download(context.Background(), s.client, "hello", 3, work, "")
		if !errors.Is(err, ErrSignature) || f != nil {
			t.Errorf("%s: file %v, err = %v", name, f, err)
		}
		if entries, _ := os.ReadDir(work); len(entries) != 0 {
			t.Errorf("%s: left %v", name, entries)
		}
	}
}

// The signature is checked on the uncompressed content: the same tar
// compressed by another zlib keeps its signature.
func TestSignatureCoversTheTarNotTheGzip(t *testing.T) {
	tarBytes := []byte(strings.Repeat("tar content ", 100))
	signedArchive := gzipped(t, tarBytes)
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.NoCompression)
	gz.Write(tarBytes)
	gz.Close()
	other := buf.Bytes()
	if bytes.Equal(other, signedArchive) {
		t.Fatal("same gzip bytes")
	}
	id, sig := signWith(t, testKey, "hello", 3, signedArchive)
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("X-Sagnac-Kit-Sha256", sum(other))
		w.Header().Set(headerKeyID, id)
		w.Header().Set(headerSignature, sig)
		w.Write(other)
	})
	f, err := Download(context.Background(), s.client, "hello", 3, t.TempDir(), "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	Discard(f)
}

// A gzip bomb is stopped while the digest is computed.
func TestTarDigestBounded(t *testing.T) {
	archive := gzipped(t, make([]byte, 1<<20))
	var unsafe *UnsafeError
	if _, err := TarDigest(bytes.NewReader(archive), 1024); !errors.As(err, &unsafe) || unsafe.Reason != reasonTooLarge {
		t.Errorf("err = %v", err)
	}
}

// The keys pinned in the binary decode, and a release pins two of them (the
// key in use and the emergency key): SAGNAC_RELEASE_KEYS=1, set by release.yml.
func TestPinnedKeys(t *testing.T) {
	for _, k := range pinnedKeys {
		if len(k.Key) != ed25519.PublicKeySize || k.ID != KeyID(k.Key) || len(k.ID) != 16 {
			t.Errorf("key %s malformed", k.ID)
		}
	}
	if os.Getenv("SAGNAC_RELEASE_KEYS") == "1" && len(pinnedKeys) < 2 {
		t.Errorf("%d kit signing keys pinned, a release needs the key in use and the emergency key", len(pinnedKeys))
	}
}

func TestPinnedRefusesAMalformedKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a malformed key was accepted")
		}
	}()
	pinned(base64.StdEncoding.EncodeToString(make([]byte, 31)))
}
