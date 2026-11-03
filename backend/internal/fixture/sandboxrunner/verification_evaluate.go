package sandboxrunner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Every verification backend runs the same three commands in the patched checkout and
// hands their output here, where the three checks are decided the same way whatever ran
// them. The module list comes first: when the build list cannot be resolved against the
// offline module cache, the patch names a module the mirror does not hold, which is not a
// test failure but a verification that cannot reach a verdict.

// Step names, in the order a verification runs them.
const (
	stepModules = "go list -m -json all"
	stepTests   = "go test -json ./..."
	stepScan    = "vulnerability scan"
)

// Scanner names reported in a summary.
const (
	scannerGovulncheck = "govulncheck"
	scannerSnapshot    = "snapshot-modules"
)

// maxStepOutputBytes bounds the output one step may contribute to a verification.
const maxStepOutputBytes = 4 << 20

// stepResult is one command's outcome. Err is set only when the command did not run to an
// exit status, for example because the context ended.
type stepResult struct {
	Name     string
	ExitCode int
	Output   []byte
	Err      error
}

// scanResult is a vulnerability scan's outcome as the evaluation needs it.
type scanResult struct {
	Scanner  string
	Ran      bool
	Findings []string
}

// verificationSteps are the raw results a backend collected.
type verificationSteps struct {
	Preparation []byte
	Modules     stepResult
	Tests       stepResult
	Scan        scanResult
	ScanOutput  []byte
	// ModuleFilesChanged reports that go.mod or go.sum differed after the steps from what
	// the patch produced; the verification then reports no verdict.
	ModuleFilesChanged bool
}

// moduleReferencePattern finds the module@version the go command names when the build list
// cannot be resolved offline.
var moduleReferencePattern = regexp.MustCompile(`([A-Za-z0-9._~/-]+@v[0-9][A-Za-z0-9.+-]*)`)

// evaluateVerification decides the three checks from the steps against snapshot. It
// writes the combined log and never reads anything but the step outputs.
func evaluateVerification(steps verificationSteps, snapshot *vulnSnapshot) VerificationOutcome {
	var log bytes.Buffer
	log.Write(steps.Preparation)
	writeStep(&log, steps.Modules)
	outcome := VerificationOutcome{Scanner: steps.Scan.Scanner, SnapshotDigest: snapshot.digest}

	if steps.Modules.Err != nil {
		outcome.Log = log.Bytes()
		return failed(outcome, codeSandboxError, "module list could not be run")
	}
	if steps.Modules.ExitCode != 0 {
		outcome.Log = log.Bytes()
		return failed(outcome, codeModuleUnavailable, moduleUnavailableMessage(steps.Modules.Output))
	}
	modules, err := parseModuleList(steps.Modules.Output)
	if err != nil {
		outcome.Log = log.Bytes()
		return failed(outcome, codeSandboxError, "module list could not be read")
	}
	vulnerable := snapshot.vulnerableDependencies(modules)
	for _, dep := range vulnerable {
		outcome.VulnerableDependencies = append(outcome.VulnerableDependencies, dep.String())
	}
	outcome.DependenciesClean = len(vulnerable) == 0

	writeStep(&log, steps.Tests)
	if steps.Tests.Err != nil {
		outcome.Log = log.Bytes()
		return failed(outcome, codeSandboxError, "tests could not be run")
	}
	total, failedTests := countTests(steps.Tests.Output)
	outcome.Tests = testCounts{Total: total, Failed: failedTests}
	outcome.TestsPassed = steps.Tests.ExitCode == 0 && failedTests == 0

	fmt.Fprintf(&log, "$ %s (%s)\n", stepScan, steps.Scan.Scanner)
	log.Write(steps.ScanOutput)
	outcome.ScanRan = steps.Scan.Ran
	outcome.ScanFindings = slices.Clone(steps.Scan.Findings)
	outcome.ScanClean = steps.Scan.Ran && len(steps.Scan.Findings) == 0

	scanSummary := fmt.Sprintf("%s, %d findings", steps.Scan.Scanner, len(steps.Scan.Findings))
	if !steps.Scan.Ran {
		// A scan that produced no result cannot clear the patch: the reason stays in the
		// summary so a failed verification is not mistaken for a vulnerable one.
		scanSummary = steps.Scan.Scanner + " did not run to a result"
	}
	fmt.Fprintf(&log, "\ntests passed: %t (%d tests, %d failed)\nscan clean: %t (%s)\ndependencies clean: %t (%d vulnerable, snapshot %s)\n",
		outcome.TestsPassed, total, failedTests, outcome.ScanClean, scanSummary, outcome.DependenciesClean, len(vulnerable), snapshot.digest)
	for _, dep := range outcome.VulnerableDependencies {
		fmt.Fprintf(&log, "  vulnerable dependency: %s\n", dep)
	}
	outcome.Log = log.Bytes()
	if steps.ModuleFilesChanged {
		return failed(outcome, codeSandboxError, "go.mod or go.sum changed during the verification")
	}
	outcome.Verdict = true
	return outcome
}

func failed(outcome VerificationOutcome, code, message string) VerificationOutcome {
	outcome.Verdict = false
	outcome.FailureCode = code
	outcome.FailureMessage = message
	return outcome
}

// moduleUnavailableMessage names the module the go command could not resolve offline, when
// its output names one.
func moduleUnavailableMessage(output []byte) string {
	if match := moduleReferencePattern.Find(output); match != nil {
		return "module " + string(match) + " is not available in the offline module cache"
	}
	return "the module graph could not be resolved from the offline module cache"
}

func writeStep(log *bytes.Buffer, step stepResult) {
	fmt.Fprintf(log, "$ %s\n", step.Name)
	log.Write(step.Output)
	if len(step.Output) > 0 && step.Output[len(step.Output)-1] != '\n' {
		log.WriteByte('\n')
	}
	switch {
	case step.Err != nil:
		fmt.Fprintf(log, "(did not finish: %v)\n", step.Err)
	default:
		fmt.Fprintf(log, "(exit status %d)\n", step.ExitCode)
	}
}

// govulncheckFinding is the part of one `govulncheck -json` message the scan needs.
type govulncheckFinding struct {
	Finding *struct {
		OSV string `json:"osv"`
	} `json:"finding"`
}

// parseGovulncheckFindings reads the JSON stream `govulncheck -json` writes and returns the
// advisory ids it reported, deduplicated in first-seen order. A finding at any level, the
// module list included, counts: the verification is of the module's dependency set, not of
// which symbols a call reaches, so its verdict agrees with the module comparison.
func parseGovulncheckFindings(output []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var findings []string
	for decoder.More() {
		var message govulncheckFinding
		if err := decoder.Decode(&message); err != nil {
			return nil, fmt.Errorf("govulncheck output is not a stream of JSON objects: %w", err)
		}
		if message.Finding == nil || message.Finding.OSV == "" {
			continue
		}
		if !slices.Contains(findings, message.Finding.OSV) {
			findings = append(findings, message.Finding.OSV)
		}
	}
	return findings, nil
}

// snapshotScan compares the module list against the snapshot, standing in for govulncheck
// where it is not installed. Its findings are the affected advisories.
func snapshotScan(modulesOutput []byte, snapshot *vulnSnapshot) (scanResult, []byte) {
	modules, err := parseModuleList(modulesOutput)
	if err != nil {
		return scanResult{Scanner: scannerSnapshot}, []byte("module list could not be read\n")
	}
	result := scanResult{Scanner: scannerSnapshot, Ran: true}
	var output bytes.Buffer
	for _, dep := range snapshot.vulnerableDependencies(modules) {
		if !slices.Contains(result.Findings, dep.Advisory) {
			result.Findings = append(result.Findings, dep.Advisory)
		}
		fmt.Fprintf(&output, "%s: %s@%s\n", dep.Advisory, dep.Module, dep.Version)
	}
	if len(result.Findings) == 0 {
		output.WriteString("No vulnerabilities found.\n")
	}
	return result, output.Bytes()
}

// splitSentinelSteps parses the output of the container script, which separates each
// step's output with sentinel lines, into the named steps. A step without an exit line
// did not finish.
func splitSentinelSteps(output []byte) (map[string]stepResult, []byte) {
	steps := make(map[string]stepResult)
	var preamble bytes.Buffer
	var current *stepResult
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64<<10), maxStepOutputBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		switch {
		case bytes.HasPrefix(line, []byte(sentinelStep)):
			if current != nil {
				current.Err = errStepUnfinished
				steps[current.Name] = *current
			}
			current = &stepResult{Name: string(bytes.TrimSpace(line[len(sentinelStep):]))}
		case bytes.HasPrefix(line, []byte(sentinelExit)) && current != nil:
			code := 0
			_, err := fmt.Sscanf(string(bytes.TrimSpace(line[len(sentinelExit):])), "%d", &code)
			if err != nil {
				current.Err = errStepUnfinished
			}
			current.ExitCode = code
			steps[current.Name] = *current
			current = nil
		case current != nil:
			current.Output = append(current.Output, line...)
			current.Output = append(current.Output, '\n')
		default:
			preamble.Write(line)
			preamble.WriteByte('\n')
		}
	}
	if current != nil {
		current.Err = errStepUnfinished
		steps[current.Name] = *current
	}
	return steps, preamble.Bytes()
}

// Sentinels the container script writes around each step. The record separator byte
// cannot appear in text output of the go tools.
const (
	sentinelStep = "\x1eSTEP "
	sentinelExit = "\x1eEXIT "
)

var errStepUnfinished = fmt.Errorf("step did not report an exit status")

// Step names used inside the container script.
const (
	scriptStepModules      = "modules"
	scriptStepTests        = "tests"
	scriptStepScan         = "scan"
	scriptStepModfilesPre  = "modfiles-before"
	scriptStepModfilesPost = "modfiles-after"
)

// stepsFromScript maps the container script's named steps onto verificationSteps.
func stepsFromScript(named map[string]stepResult, preamble []byte, preparation []byte) verificationSteps {
	rename := func(key, name string) stepResult {
		step, ok := named[key]
		if !ok {
			return stepResult{Name: name, Err: errStepUnfinished}
		}
		step.Name = name
		return step
	}
	steps := verificationSteps{
		Preparation: append(append([]byte{}, preparation...), preamble...),
		Modules:     rename(scriptStepModules, stepModules),
		Tests:       rename(scriptStepTests, stepTests),
	}
	scan := rename(scriptStepScan, stepScan)
	steps.ScanOutput = scan.Output
	steps.Scan = scanResult{Scanner: scannerGovulncheck}
	if scan.Err == nil && scan.ExitCode == 0 {
		if findings, err := parseGovulncheckFindings(scan.Output); err == nil {
			steps.Scan.Ran = true
			steps.Scan.Findings = findings
		}
	}
	before, after := named[scriptStepModfilesPre], named[scriptStepModfilesPost]
	steps.ModuleFilesChanged = before.Err != nil || after.Err != nil || !bytes.Equal(before.Output, after.Output) ||
		strings.TrimSpace(string(before.Output)) == ""
	return steps
}
