package config

import "strings"

// KeyVerifyRunnerBaseURL is the base URL of the sandbox runner the verify_patch Tool
// dispatches verifications to. It is optional: when it is unset the Tool is not registered,
// so a Definition that names it fails explicitly instead of dispatching to nothing. It is a
// separate key from the test runner's because the two Tools may be served by different
// runner processes; in the demo both point at the same one.
const KeyVerifyRunnerBaseURL = EnvPrefix + "VERIFY_RUNNER_BASE_URL"

// VerifyRunner locates the sandbox runner that verifies patches. The zero value means no
// runner is configured.
type VerifyRunner struct {
	BaseURL string
}

// Enabled reports whether a runner is configured.
func (v VerifyRunner) Enabled() bool {
	return v.BaseURL != ""
}

// verifyRunner reads the optional runner location. A set value must be a base URL that
// carries no credential of its own; an invalid one rejects the configuration.
func (l *loader) verifyRunner() VerifyRunner {
	if strings.TrimSpace(l.env(KeyVerifyRunnerBaseURL)) == "" {
		return VerifyRunner{}
	}
	return VerifyRunner{BaseURL: l.requiredProviderURL(KeyVerifyRunnerBaseURL)}
}
