package oauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidState = errors.New("oauth: invalid state")
	ErrStateExpired = errors.New("oauth: state expired")
)

// StateSigner signs and verifies opaque state values that carry the phone
// number, a random nonce and an expiration timestamp.
type StateSigner struct {
	secret []byte
}

func NewStateSigner(secret string) *StateSigner {
	return &StateSigner{secret: []byte(secret)}
}

type StatePayload struct {
	Phone string
	Nonce string
	Exp   time.Time
}

// New builds a new signed state for the given phone with the configured TTL.
func (s *StateSigner) New(phone string, ttl time.Duration) (string, StatePayload, error) {
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		return "", StatePayload{}, err
	}
	p := StatePayload{
		Phone: phone,
		Nonce: base64.RawURLEncoding.EncodeToString(nb),
		Exp:   time.Now().Add(ttl).UTC(),
	}
	raw := s.payloadString(p)
	mac := s.sign(raw)
	encoded := base64.RawURLEncoding.EncodeToString([]byte(raw + "." + mac))
	return encoded, p, nil
}

// Verify decodes the signed state and validates the HMAC and expiration.
func (s *StateSigner) Verify(state string) (StatePayload, error) {
	dec, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return StatePayload{}, ErrInvalidState
	}
	parts := strings.Split(string(dec), ".")
	if len(parts) != 4 {
		// payload = phone.nonce.exp (3 parts), + mac = 4 parts total
		return StatePayload{}, ErrInvalidState
	}
	raw := strings.Join(parts[:3], ".")
	mac := parts[3]
	expected := s.sign(raw)
	if !hmac.Equal([]byte(mac), []byte(expected)) {
		return StatePayload{}, ErrInvalidState
	}
	expUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return StatePayload{}, ErrInvalidState
	}
	p := StatePayload{
		Phone: parts[0],
		Nonce: parts[1],
		Exp:   time.Unix(expUnix, 0).UTC(),
	}
	if time.Now().After(p.Exp) {
		return p, ErrStateExpired
	}
	return p, nil
}

func (s *StateSigner) payloadString(p StatePayload) string {
	return fmt.Sprintf("%s.%s.%d", p.Phone, p.Nonce, p.Exp.Unix())
}

func (s *StateSigner) sign(raw string) string {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// NewLinkToken returns a short, URL-safe token used by the /c/{token} short
// link redirect. ~13 base32 chars from 8 random bytes.
func NewLinkToken() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	enc := base32Alphabet().EncodeToString(buf)
	return strings.TrimRight(enc, "="), nil
}
