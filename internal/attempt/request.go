package attempt

import (
	"encoding/json"
	"net/http"
)

// Values of the headers of every call to the platform. post builds the real
// request from them and Describe shows them: one source, so that what
// "sagnac check --show-payload" prints is what is sent.
const (
	contentType = "application/json"
	accept      = "application/json"
)

// Paths of the two calls.
const (
	SessionsPath = "/v1/sessions"
	AttemptsPath = "/v1/attempts"
)

// SessionBody is the body of POST /v1/sessions.
func SessionBody(lab string, version int64, checksSHA256 string) ([]byte, error) {
	return json.Marshal(map[string]any{"lab": lab, "version": version, "checks_sha256": checksSHA256})
}

// Request describes a call to the platform without making it.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// Placeholders stand for the two header values that are not known before the
// call: the token, which is never shown, and the idempotency key, drawn at
// the moment of the sending.
type Placeholders struct {
	Authorization string
	Key           string
}

// Describe shows the call that post makes to path with body: the headers
// post sets, the User-Agent of the client, and the placeholders for the
// token and, for an attempt, the key. body must be valid JSON.
func Describe(api, path, userAgent string, body []byte, p Placeholders) Request {
	h := http.Header{}
	setCommon(h)
	headers := map[string]string{
		"Content-Type":  h.Get("Content-Type"),
		"Accept":        h.Get("Accept"),
		"User-Agent":    userAgent,
		"Authorization": p.Authorization,
	}
	if path == AttemptsPath {
		headers["Idempotency-Key"] = p.Key
	}
	return Request{Method: http.MethodPost, URL: api + path, Headers: headers, Body: json.RawMessage(body)}
}

// setCommon sets the headers that every call carries, whatever its body.
func setCommon(h http.Header) {
	h.Set("Content-Type", contentType)
	h.Set("Accept", accept)
}
