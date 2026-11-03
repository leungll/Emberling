// Package auth issues and verifies the signed access tokens of the token service. The
// HTTP layer depends only on this package's exported API: Issuer, Identity and the three
// sentinel errors.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt"
)

// Verification errors. A caller compares with errors.Is; the message never carries the
// token.
var (
	// ErrExpired reports a well-formed token whose expiry has passed.
	ErrExpired = errors.New("auth: token has expired")
	// ErrAudience reports a valid token issued for another audience.
	ErrAudience = errors.New("auth: token is not for this audience")
	// ErrInvalid reports a token that is malformed, tampered with or signed another way.
	ErrInvalid = errors.New("auth: token is invalid")
)

// minSecretBytes is the shortest HMAC secret an Issuer accepts.
const minSecretBytes = 16

// Issuer signs tokens for one audience with one shared secret and verifies them again.
type Issuer struct {
	secret   []byte
	audience string
	ttl      time.Duration
}

// Identity is what a verified token says about its holder.
type Identity struct {
	Subject   string
	ExpiresAt time.Time
}

// NewIssuer returns an Issuer for audience whose tokens live for ttl.
func NewIssuer(secret []byte, audience string, ttl time.Duration) (*Issuer, error) {
	switch {
	case len(secret) < minSecretBytes:
		return nil, fmt.Errorf("auth: secret must be at least %d bytes", minSecretBytes)
	case audience == "":
		return nil, errors.New("auth: audience is required")
	case ttl <= 0:
		return nil, errors.New("auth: ttl must be positive")
	}
	return &Issuer{secret: secret, audience: audience, ttl: ttl}, nil
}

// Issue signs a token for subject that expires ttl after now.
func (i *Issuer) Issue(subject string, now time.Time) (string, error) {
	if subject == "" {
		return "", errors.New("auth: subject is required")
	}
	claims := jwt.StandardClaims{
		Subject:   subject,
		Audience:  i.audience,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(i.ttl).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
	if err != nil {
		return "", fmt.Errorf("auth: sign token: %w", err)
	}
	return signed, nil
}

// Verify checks the signature, the expiry and the audience of token and returns the
// identity it carries.
func (i *Issuer) Verify(token string) (Identity, error) {
	claims := &jwt.StandardClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return i.secret, nil
	})
	if err != nil {
		var validation *jwt.ValidationError
		if errors.As(err, &validation) && validation.Errors&jwt.ValidationErrorExpired != 0 {
			return Identity{}, ErrExpired
		}
		return Identity{}, ErrInvalid
	}
	if !claims.VerifyAudience(i.audience, true) {
		return Identity{}, ErrAudience
	}
	return Identity{Subject: claims.Subject, ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC()}, nil
}
