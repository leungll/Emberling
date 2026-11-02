package config

import (
	"errors"
	"strings"
	"testing"
)

const gatewayModelsValue = `[{"id":"gateway-seedream-5.0-pro","model":"bytedance-seed/seedream-5-0-pro","displayName":"Seedream 5.0 Pro (gateway)"}]`

func TestParseTaskGatewayModels_ValidList_ReturnsEntries(t *testing.T) {
	entries, err := ParseTaskGatewayModels(gatewayModelsValue)
	if err != nil {
		t.Fatalf("ParseTaskGatewayModels() error = %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "gateway-seedream-5.0-pro" || entries[0].Model != "bytedance-seed/seedream-5-0-pro" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseTaskGatewayModels_InvalidList_RejectedWithoutValues(t *testing.T) {
	for name, value := range map[string]string{
		"not json":       `secret-looking-value`,
		"without prefix": `[{"id":"seedream-secret","model":"m/x","displayName":"X"}]`,
		"uppercase id":   `[{"id":"gateway-Secret","model":"m/x","displayName":"X"}]`,
		"unknown member": `[{"id":"gateway-x","model":"m/x","displayName":"X","apiKey":"secret"}]`,
		"empty":          `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseTaskGatewayModels(value)
			var invalid *InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != KeyTaskGatewayModels {
				t.Fatalf("error = %v, want an InvalidConfigError for %s", err, KeyTaskGatewayModels)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "Secret") {
				t.Fatalf("error %q quotes the configured value", err)
			}
		})
	}
}
