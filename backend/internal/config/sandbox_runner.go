package config

import "strings"

// KeySandboxRunnerBaseURL is the base URL of the sandbox test runner the sandbox_test Tool
// dispatches to. It is optional: when it is unset the Tool is not registered.
const KeySandboxRunnerBaseURL = EnvPrefix + "SANDBOX_RUNNER_BASE_URL"

// SandboxRunner locates the sandbox test runner. The zero value means the runner is not
// configured.
type SandboxRunner struct {
	BaseURL string
}

// Enabled reports whether a runner is configured.
func (s SandboxRunner) Enabled() bool {
	return s.BaseURL != ""
}

// sandboxRunner reads the optional runner location. A set value must be a base URL that
// carries no credential of its own.
func (l *loader) sandboxRunner() SandboxRunner {
	if strings.TrimSpace(l.env(KeySandboxRunnerBaseURL)) == "" {
		return SandboxRunner{}
	}
	return SandboxRunner{BaseURL: l.requiredProviderURL(KeySandboxRunnerBaseURL)}
}
