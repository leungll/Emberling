package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// KeyProviderCapacity bounds how many tasks each asynchronous Provider may have in flight
// at once, as a comma-separated list of `<provider id>=<limit>` entries such as
// `task-gateway-v1=5,mock-task-provider-v1=5`. Empty means no Provider is limited. The
// limit is read once at startup: it is not stored, and a running process never changes
// it.
const KeyProviderCapacity = EnvPrefix + "PROVIDER_CAPACITY"

// ParseProviderCapacity parses the capacity list. Every entry must name a non-empty
// Provider id once and a positive integer limit; the error names the entry index and the
// rule, never the value.
func ParseProviderCapacity(value string) (map[string]int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	entries := strings.Split(value, ",")
	limits := make(map[string]int, len(entries))
	var problems []string
	for i, entry := range entries {
		entry = strings.TrimSpace(entry)
		id, limitText, found := strings.Cut(entry, "=")
		id = strings.TrimSpace(id)
		limitText = strings.TrimSpace(limitText)
		switch {
		case !found || id == "" || limitText == "":
			problems = append(problems, fmt.Sprintf("entry %d: must be <provider id>=<limit>", i))
			continue
		case strings.ContainsAny(id, " \t"):
			problems = append(problems, fmt.Sprintf("entry %d: provider id must not contain whitespace", i))
			continue
		}
		limit, err := strconv.Atoi(limitText)
		if err != nil {
			problems = append(problems, fmt.Sprintf("entry %d: limit must be an integer", i))
			continue
		}
		if limit <= 0 {
			problems = append(problems, fmt.Sprintf("entry %d: limit must be a positive integer", i))
			continue
		}
		if _, duplicate := limits[id]; duplicate {
			problems = append(problems, fmt.Sprintf("entry %d: provider id repeats an earlier entry", i))
			continue
		}
		limits[id] = limit
	}
	if len(problems) > 0 {
		return nil, &InvalidConfigError{Key: KeyProviderCapacity, Reason: strings.Join(problems, "; ")}
	}
	return limits, nil
}

// providerCapacity reads the optional capacity list.
func (l *loader) providerCapacity() map[string]int {
	limits, err := ParseProviderCapacity(l.env(KeyProviderCapacity))
	if err != nil {
		var invalid *InvalidConfigError
		if errors.As(err, &invalid) {
			l.fail(KeyProviderCapacity, invalid.Reason)
		}
		return nil
	}
	return limits
}

// validateProviderCapacity re-checks what ParseProviderCapacity enforces, for a Config
// assembled by another path.
func validateProviderCapacity(limits map[string]int) error {
	for id, limit := range limits {
		if strings.TrimSpace(id) == "" {
			return &InvalidConfigError{Key: KeyProviderCapacity, Reason: "provider id must not be empty"}
		}
		if limit <= 0 {
			return &InvalidConfigError{Key: KeyProviderCapacity, Reason: "limit must be a positive integer"}
		}
	}
	return nil
}
