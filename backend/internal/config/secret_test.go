package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
)

const plaintext = "super-secret-callback-signing-key"

func TestSecret_String_Redacts(t *testing.T) {
	secret := config.NewSecret(plaintext)

	for name, rendered := range map[string]string{
		"String":    secret.String(),
		"%v":        fmt.Sprintf("%v", secret),
		"%s":        fmt.Sprintf("%s", secret), //nolint:staticcheck // S1025: the test exercises the %s verb, not String()
		"%#v":       fmt.Sprintf("%#v", secret),
		"in struct": fmt.Sprintf("%v", struct{ S config.Secret }{secret}),
	} {
		if strings.Contains(rendered, plaintext) {
			t.Errorf("%s: leaks the secret: %q", name, rendered)
		}
	}
	if secret.Reveal() != plaintext {
		t.Error("Reveal: want the plaintext value back")
	}
}

func TestSecret_MarshalJSON_Redacts(t *testing.T) {
	encoded, err := json.Marshal(struct {
		Token config.Secret `json:"token"`
	}{config.NewSecret(plaintext)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), plaintext) {
		t.Fatalf("marshalled secret leaks the value: %s", encoded)
	}
}

func TestSecret_SlogLogValue_Redacts(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	logger.Info("provider call", slog.Any("api_key", config.NewSecret(plaintext)))

	if strings.Contains(buf.String(), plaintext) {
		t.Fatalf("log line leaks the secret: %s", buf.String())
	}
}
