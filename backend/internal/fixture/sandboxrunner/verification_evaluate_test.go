package sandboxrunner

import (
	"strings"
	"testing"
)

const (
	modulesClean = `{"Path":"example.com/tokenservice","Main":true}
{"Path":"github.com/golang-jwt/jwt/v5","Version":"v5.3.1"}
`
	modulesIndirectVulnerable = modulesClean + `{"Path":"github.com/golang-jwt/jwt","Version":"v3.2.2+incompatible","Indirect":true}
`
	testsAllPass = `{"Action":"run","Package":"example.com/tokenservice/tests","Test":"TestA"}
{"Action":"pass","Package":"example.com/tokenservice/tests","Test":"TestA"}
{"Action":"pass","Package":"example.com/tokenservice/tests","Test":"TestB"}
`
	govulncheckOutput = `{"config":{"protocol_version":"v1.0.0","scanner_name":"govulncheck"}}
{"osv":{"id":"GO-2025-3553"}}
{"finding":{"osv":"GO-2025-3553","fixed_version":"v5.2.2","trace":[{"module":"github.com/golang-jwt/jwt","version":"v3.2.2+incompatible"}]}}
{"finding":{"osv":"GO-2025-3553","fixed_version":"v5.2.2","trace":[{"module":"github.com/golang-jwt/jwt","version":"v3.2.2+incompatible","package":"github.com/golang-jwt/jwt","function":"Parse"}]}}
`
)

func mustSnapshot(t *testing.T) *vulnSnapshot {
	t.Helper()
	snapshot, err := loadVulnSnapshot()
	if err != nil {
		t.Fatalf("loadVulnSnapshot: %v", err)
	}
	return snapshot
}

func passingSteps(modules string, scan scanResult) verificationSteps {
	return verificationSteps{
		Modules: stepResult{Name: stepModules, Output: []byte(modules)},
		Tests:   stepResult{Name: stepTests, Output: []byte(testsAllPass)},
		Scan:    scan,
	}
}

// A passing test suite with a scan that still reports the advisory, as a mock scanner
// injects it, must not pass: the three checks are independent.
func TestVerification_ScanFindsVulnerability_PassedFalse(t *testing.T) {
	steps := passingSteps(modulesClean, scanResult{Scanner: scannerGovulncheck, Ran: true, Findings: []string{"GO-2025-3553"}})
	outcome := evaluateVerification(steps, mustSnapshot(t))
	if !outcome.Verdict || !outcome.TestsPassed || !outcome.DependenciesClean {
		t.Fatalf("outcome = %+v, want a verdict with passing tests and clean dependencies", outcome)
	}
	if outcome.ScanClean || outcome.Passed() || len(outcome.ScanFindings) != 1 {
		t.Fatalf("outcome = %+v, want scanClean false and passed false", outcome)
	}
}

// An indirect dependency left at an affected version fails the dependency check even when
// the tests pass and a reachability-based scan reports nothing.
func TestVerification_VulnerableIndirectDependency_DependenciesCleanFalse(t *testing.T) {
	steps := passingSteps(modulesIndirectVulnerable, scanResult{Scanner: scannerGovulncheck, Ran: true})
	outcome := evaluateVerification(steps, mustSnapshot(t))
	if !outcome.Verdict || !outcome.TestsPassed || !outcome.ScanClean {
		t.Fatalf("outcome = %+v, want a verdict with passing tests and a clean scan", outcome)
	}
	if outcome.DependenciesClean || outcome.Passed() {
		t.Fatalf("outcome = %+v, want dependenciesClean false and passed false", outcome)
	}
	if len(outcome.VulnerableDependencies) != 1 || outcome.VulnerableDependencies[0] != "github.com/golang-jwt/jwt@v3.2.2+incompatible (GO-2025-3553, indirect)" {
		t.Fatalf("vulnerable dependencies = %v", outcome.VulnerableDependencies)
	}
	if !strings.Contains(string(outcome.Log), "vulnerable dependency: github.com/golang-jwt/jwt@v3.2.2+incompatible") {
		t.Fatalf("log lacks the dependency:\n%s", outcome.Log)
	}
}

func TestEvaluateVerification_AllThreeChecksHold_Passes(t *testing.T) {
	outcome := evaluateVerification(passingSteps(modulesClean, scanResult{Scanner: scannerGovulncheck, Ran: true}), mustSnapshot(t))
	if !outcome.Verdict || !outcome.Passed() || outcome.Tests != (testCounts{Total: 2}) {
		t.Fatalf("outcome = %+v, want all three checks to hold with 2 tests", outcome)
	}
}

func TestEvaluateVerification_ScanDidNotRun_ScanNotClean(t *testing.T) {
	outcome := evaluateVerification(passingSteps(modulesClean, scanResult{Scanner: scannerGovulncheck}), mustSnapshot(t))
	if !outcome.Verdict || outcome.ScanClean || outcome.ScanRan || outcome.Passed() {
		t.Fatalf("outcome = %+v, want a verdict whose scan is not clean", outcome)
	}
	if !strings.Contains(string(outcome.Log), "scan clean: false (govulncheck did not run to a result)") {
		t.Fatalf("log does not state why the scan is not clean:\n%s", outcome.Log)
	}
}

func TestEvaluateVerification_ModuleListFails_ReportsTheMissingModule(t *testing.T) {
	steps := verificationSteps{Modules: stepResult{Name: stepModules, ExitCode: 1, Output: []byte("go: github.com/golang-jwt/jwt/v4@v4.5.2: missing go.sum entry for go.mod file; to add it:\n\tgo mod download github.com/golang-jwt/jwt/v4\n")}}
	outcome := evaluateVerification(steps, mustSnapshot(t))
	if outcome.Verdict || outcome.FailureCode != codeModuleUnavailable {
		t.Fatalf("outcome = %+v, want %s", outcome, codeModuleUnavailable)
	}
	if outcome.FailureMessage != "module github.com/golang-jwt/jwt/v4@v4.5.2 is not available in the offline module cache" {
		t.Fatalf("message = %q", outcome.FailureMessage)
	}
}

func TestEvaluateVerification_ModuleFilesChanged_NoVerdict(t *testing.T) {
	steps := passingSteps(modulesClean, scanResult{Scanner: scannerGovulncheck, Ran: true})
	steps.ModuleFilesChanged = true
	outcome := evaluateVerification(steps, mustSnapshot(t))
	if outcome.Verdict || outcome.FailureCode != codeSandboxError || !strings.Contains(outcome.FailureMessage, "go.mod or go.sum changed") {
		t.Fatalf("outcome = %+v, want no verdict because the module files changed", outcome)
	}
}

func TestParseGovulncheckFindings_DeduplicatesAdvisoriesAtEveryLevel(t *testing.T) {
	findings, err := parseGovulncheckFindings([]byte(govulncheckOutput))
	if err != nil || len(findings) != 1 || findings[0] != "GO-2025-3553" {
		t.Fatalf("findings = %v, err = %v", findings, err)
	}
	clean, err := parseGovulncheckFindings([]byte(`{"config":{}}` + "\n" + `{"progress":{"message":"Scanning"}}` + "\n"))
	if err != nil || len(clean) != 0 {
		t.Fatalf("clean findings = %v, err = %v", clean, err)
	}
}

func TestSplitSentinelSteps_RebuildsEachStep(t *testing.T) {
	output := "preamble line\n\x1eSTEP modules\n{\"Path\":\"m\"}\n\x1eEXIT 0\n\x1eSTEP tests\nbuild failed\n\x1eEXIT 1\n\x1eSTEP scan\npartial"
	steps, preamble := splitSentinelSteps([]byte(output))
	if string(preamble) != "preamble line\n" {
		t.Fatalf("preamble = %q", preamble)
	}
	if steps["modules"].ExitCode != 0 || string(steps["modules"].Output) != "{\"Path\":\"m\"}\n" {
		t.Fatalf("modules = %+v", steps["modules"])
	}
	if steps["tests"].ExitCode != 1 || steps["tests"].Err != nil {
		t.Fatalf("tests = %+v", steps["tests"])
	}
	if steps["scan"].Err == nil {
		t.Fatalf("scan = %+v, want an unfinished step", steps["scan"])
	}
}

func TestStepsFromScript_ScanAndModuleFileHashes(t *testing.T) {
	named := map[string]stepResult{
		scriptStepModfilesPre:  {Output: []byte("abc go.mod\ndef go.sum\n")},
		scriptStepModfilesPost: {Output: []byte("abc go.mod\ndef go.sum\n")},
		scriptStepModules:      {Output: []byte(modulesClean)},
		scriptStepTests:        {Output: []byte(testsAllPass)},
		scriptStepScan:         {Output: []byte(govulncheckOutput)},
	}
	steps := stepsFromScript(named, nil, []byte("prep\n"))
	if steps.ModuleFilesChanged || !steps.Scan.Ran || len(steps.Scan.Findings) != 1 || steps.Scan.Scanner != scannerGovulncheck {
		t.Fatalf("steps = %+v", steps)
	}
	named[scriptStepModfilesPost] = stepResult{Output: []byte("abc go.mod\nchanged go.sum\n")}
	if !stepsFromScript(named, nil, nil).ModuleFilesChanged {
		t.Fatal("a changed go.sum hash was not detected")
	}
	delete(named, scriptStepScan)
	if stepsFromScript(named, nil, nil).Scan.Ran {
		t.Fatal("a missing scan step counted as run")
	}
}
