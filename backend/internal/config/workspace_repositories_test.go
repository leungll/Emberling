package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
)

func workspaceRepositoriesError(t *testing.T, err error) *config.InvalidConfigError {
	t.Helper()
	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyWorkspaceRepositories {
		t.Fatalf("want InvalidConfigError for %s, got %v", config.KeyWorkspaceRepositories, err)
	}
	return invalid
}

func TestLoad_WorkspaceRepositories_ValidList_Parsed(t *testing.T) {
	env := completeEnv()
	env[config.KeyWorkspaceRepositories] = " vuln-demo=/srv/repos/vuln-demo , demo2=/srv/repos/second/ "
	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]string{"vuln-demo": "/srv/repos/vuln-demo", "demo2": "/srv/repos/second"}
	if len(cfg.WorkspaceRepositories) != len(want) {
		t.Fatalf("WorkspaceRepositories = %v, want %v", cfg.WorkspaceRepositories, want)
	}
	for id, path := range want {
		if cfg.WorkspaceRepositories[id] != path {
			t.Errorf("WorkspaceRepositories[%q] = %q, want %q", id, cfg.WorkspaceRepositories[id], path)
		}
	}
	if ids := cfg.WorkspaceRepositoryIDs(); len(ids) != 2 || ids[0] != "demo2" || ids[1] != "vuln-demo" {
		t.Errorf("WorkspaceRepositoryIDs() = %v, want [demo2 vuln-demo]", ids)
	}
}

func TestLoad_WorkspaceRepositories_Empty_NoRepositories(t *testing.T) {
	for name, value := range map[string]string{"unset": "", "blank": "   "} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			if value != "" {
				env[config.KeyWorkspaceRepositories] = value
			}
			cfg, err := config.Load(lookup(env))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if len(cfg.WorkspaceRepositories) != 0 || len(cfg.WorkspaceRepositoryIDs()) != 0 {
				t.Fatalf("WorkspaceRepositories = %v, want none", cfg.WorkspaceRepositories)
			}
		})
	}
}

func TestLoad_WorkspaceRepositories_InvalidRepositoryID_Rejected(t *testing.T) {
	for name, value := range map[string]string{
		"uppercase":        "Vuln-Demo=/srv/repos/vuln-demo",
		"leading digit":    "1demo=/srv/repos/demo",
		"leading hyphen":   "-demo=/srv/repos/demo",
		"underscore":       "vuln_demo=/srv/repos/demo",
		"inner whitespace": "vuln demo=/srv/repos/demo",
		"no equals":        "vuln-demo",
		"empty id":         "=/srv/repos/demo",
	} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			env[config.KeyWorkspaceRepositories] = value
			_, err := config.Load(lookup(env))
			invalid := workspaceRepositoriesError(t, err)
			if !strings.Contains(invalid.Reason, "entry 0") {
				t.Errorf("Reason = %q, want it to name entry 0", invalid.Reason)
			}
			if strings.Contains(invalid.Reason, "/srv/repos") {
				t.Errorf("Reason = %q repeats the configured value", invalid.Reason)
			}
		})
	}
}

func TestLoad_WorkspaceRepositories_RelativePath_Rejected(t *testing.T) {
	for name, value := range map[string]string{
		"relative":   "vuln-demo=repos/vuln-demo",
		"dot":        "vuln-demo=./vuln-demo",
		"empty path": "vuln-demo=",
	} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			env[config.KeyWorkspaceRepositories] = value
			_, err := config.Load(lookup(env))
			invalid := workspaceRepositoriesError(t, err)
			if !strings.Contains(invalid.Reason, "entry 0") {
				t.Errorf("Reason = %q, want it to name entry 0", invalid.Reason)
			}
		})
	}
}

func TestLoad_WorkspaceRepositories_DuplicateRepositoryID_Rejected(t *testing.T) {
	env := completeEnv()
	env[config.KeyWorkspaceRepositories] = "vuln-demo=/srv/a,other=/srv/b,vuln-demo=/srv/c"
	_, err := config.Load(lookup(env))
	invalid := workspaceRepositoriesError(t, err)
	if !strings.Contains(invalid.Reason, "entry 2: repository id repeats an earlier entry") {
		t.Errorf("Reason = %q, want the duplicate named at entry 2", invalid.Reason)
	}
}

func TestConfig_Validate_WorkspaceRepositoriesAssembledInvalid_Rejected(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg.WorkspaceRepositories = map[string]string{"Bad": "/srv/repos/bad"}
	if invalid := workspaceRepositoriesError(t, cfg.Validate()); !strings.Contains(invalid.Reason, "repository id must match") {
		t.Errorf("Reason = %q, want the identifier rule", invalid.Reason)
	}
	cfg.WorkspaceRepositories = map[string]string{"demo": "relative/path"}
	if invalid := workspaceRepositoriesError(t, cfg.Validate()); !strings.Contains(invalid.Reason, "path must be absolute") {
		t.Errorf("Reason = %q, want the absolute path rule", invalid.Reason)
	}
}
