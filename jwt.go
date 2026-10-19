package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// A JWT is three base64url parts joined by dots: header.payload.signature.
// The signature is HMAC-SHA256(header.payload) with a server secret, so the server
// can trust the payload without storing any session: change one byte and the signature breaks.

const tokenTTL = 7 * 24 * time.Hour

// We only issue HS256 tokens, so the header is a constant. Comparing it exactly
// rejects tokens that claim another algorithm (e.g. the classic "alg": "none" attack).
var jwtHeader = b64([]byte(`{"alg":"HS256","typ":"JWT"}`))

var errInvalidToken = errors.New("invalid or expired token")

type claims struct {
	Sub string `json:"sub"` // username
	Exp int64  `json:"exp"` // expiry, unix seconds
}

func signToken(username string, now time.Time) string {
	payload, _ := json.Marshal(claims{Sub: username, Exp: now.Add(tokenTTL).Unix()})
	unsigned := jwtHeader + "." + b64(payload)
	return unsigned + "." + signature(unsigned)
}

// verifyToken checks the signature and expiry and returns the username inside.
func verifyToken(token string, now time.Time) (string, error) {
	header, payload, _ := strings.Cut(token, ".")
	payload, sig, _ := strings.Cut(payload, ".")
	if header != jwtHeader || !hmac.Equal([]byte(sig), []byte(signature(header+"."+payload))) {
		return "", errInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	var c claims
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Sub == "" || now.Unix() >= c.Exp {
		return "", errInvalidToken
	}
	return c.Sub, nil
}

func signature(data string) string {
	mac := hmac.New(sha256.New, []byte(os.Getenv("JWT_SECRET")))
	mac.Write([]byte(data))
	return b64(mac.Sum(nil))
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
