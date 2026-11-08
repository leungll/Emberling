package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
)

func TestConfig_MediaServiceAbsent_IsDisabled(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MediaService.Enabled() || cfg.MediaService.BaseURL != "" {
		t.Fatalf("MediaService = %+v with no key set, want disabled", cfg.MediaService)
	}
}

func TestConfig_MediaServiceSet_LoadsBaseURL(t *testing.T) {
	env := completeEnv()
	env[config.KeyMediaServiceURL] = " http://media:9200/ "
	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MediaService.Enabled() || cfg.MediaService.BaseURL != "http://media:9200" {
		t.Fatalf("MediaService = %+v, want the configured URL without its trailing slash", cfg.MediaService)
	}
}

func TestConfig_MediaServiceInvalidURL_IsRejectedWithoutTheValue(t *testing.T) {
	for name, value := range map[string]string{
		"relative": "media:9200",
		"scheme":   "ftp://media:9200",
		"userinfo": "http://user:media-pass@media:9200",
		"query":    "http://media:9200?token=media-pass",
		"fragment": "http://media:9200#media-pass",
	} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			env[config.KeyMediaServiceURL] = value
			_, err := config.Load(lookup(env))
			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != config.KeyMediaServiceURL {
				t.Fatalf("Load error = %v, want InvalidConfigError for %s", err, config.KeyMediaServiceURL)
			}
			if strings.Contains(err.Error(), "media-pass") {
				t.Fatalf("error %q leaks the configured value", err)
			}
		})
	}
}
