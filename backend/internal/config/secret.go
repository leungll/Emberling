package config

import (
	"encoding/json"
	"log/slog"
)

// redacted is the only representation of a Secret that may leave the process. It is used
// by every rendering path so that adding a new one cannot accidentally reveal a value.
const redacted = "[REDACTED]"

// Secret holds a deployment credential: a database URL, a Provider API key or a callback
// signing secret. The value is unexported and reachable only through Reveal, so printing,
// logging or serialising a Config cannot leak it.
type Secret struct {
	value string
}

// NewSecret wraps a credential value.
func NewSecret(value string) Secret { return Secret{value: value} }

// Reveal returns the plaintext value. Call it only at the moment a credential is handed
// to the system that needs it, never to build a log line, an Event or an API response.
func (s Secret) Reveal() string { return s.value }

// IsZero reports whether no value was configured.
func (s Secret) IsZero() bool { return s.value == "" }

func (s Secret) String() string { return redacted }

// GoString covers %#v, which would otherwise print the struct field.
func (s Secret) GoString() string { return redacted }

func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// LogValue keeps Secret redacted when it is passed to slog as an attribute value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

var (
	_ slog.LogValuer = Secret{}
	_ json.Marshaler = Secret{}
)
