package mockmodel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// The video-editing scripts read the assignment the media transcript node publishes: the
// task object with the transcript reference under "transcript". Tool messages carry only
// the members a script matches on or reads through a placeholder.
const (
	videoTranscriptArtifact = "artifact_transcript_01"
	videoTranscriptSHA      = "ebf52374cf424bbd3e526887cec920552d0f1f8371cfeb9df583fb8639aa889e"
	videoSourceID           = "src_422934139a6c23fbce1947bb3099e017"
	videoAssignment         = `{"sourceId":"` + videoSourceID + `","sourceSha256":"422934139a6c23fbce1947bb3099e017d16a8b7d4e1af3c678fdc84955407403",` +
		`"durationMs":266707,"requirement":"Cut a 45-second vertical clip on why she loves engineering.","goal":"DELIVER_CLIP",` +
		`"transcript":{"artifactId":"` + videoTranscriptArtifact + `","sha256":"` + videoTranscriptSHA + `","lineCount":25,"sentenceCount":25,"sizeBytes":3000,"language":"en","durationMs":266707,"sourceId":"` + videoSourceID + `","sourceSha256":"422934139a6c23fbce1947bb3099e017d16a8b7d4e1af3c678fdc84955407403"}}`
	videoReadResult = `{"material":{"artifactId":"` + videoTranscriptArtifact + `","sha256":"` + videoTranscriptSHA + `","startLine":1,"lineCount":25,"totalLines":25},"text":"S1\t26.660\t31.740\tMusic.\n","truncated":false}`
	videoReadArgs   = `{"artifactId":"` + videoTranscriptArtifact + `","lineCount":200,"startLine":1}`
)

func timelineCheckResult(sha string, passed bool) string {
	findings := `[]`
	if !passed {
		findings = `[{"code":"TIMELINE_DURATION_OUT_OF_RANGE","level":"BLOCKING","message":"total duration 60.360 s is not within 10% of the 45.000 s target"}]`
	}
	return `{"kind":"TIMELINE","passed":` + boolText(passed) + `,"ruleVersion":"v1","subjectSha256":"` + sha + `","findings":` + findings +
		`,"timelineArtifactId":"artifact_` + sha[:32] + `","timelineSha256":"` + sha + `","durationMs":42500}`
}

func clipCheckResult(sha string) string {
	return `{"kind":"CLIP","passed":true,"ruleVersion":"v1","subjectSha256":"` + sha + `","findings":[]}`
}

func renderResult(clipSHA, timelineSHA string) string {
	return `{"clipSha256":"` + clipSHA + `","clipUrl":"http://media.test/v1/clips/` + clipSHA + `","sizeBytes":1234567,"mediaType":"video/mp4","durationMs":42500,"timelineSha256":"` + timelineSHA + `"}`
}

func contentReviewResult(timelineSHA string, passed bool) string {
	issues := `[]`
	if !passed {
		issues = `[{"type":"MEANING_DISTORTED","sentences":["S24"],"reason":"S24 stops where the speaker continues the thought in S25"}]`
	}
	return `{"passed":` + boolText(passed) + `,"policyVersion":"review_v1","timelineSha256":"` + timelineSHA + `","issues":` + issues + `}`
}

// Digests the video scripts move through: a first timeline, a revised one, the clip.
const (
	timelineA = "6d1f2e20288d88c8aa52bdff7c1c2d4abc502327ce6409897b2c23d7d05c1426"
	timelineB = "61caabbdb5a2a3ab6cc8d983411cb5b9cb848c5eee4b6f22426193fd6839b6ff"
	clipSHA   = "9c2a1f7d8e5b4a3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c"
)

func generateVideoTurn(t *testing.T, script string, toolResults ...string) (registry.ModelResponse, error) {
	t.Helper()
	request := scriptedRequest(t, script, toolResults...)
	request.Messages[0] = userMessage(t, videoAssignment)
	return NewProvider().Generate(context.Background(), request)
}

// assertTimelineCheck checks a Decision is a run_checks TIMELINE call over the assignment's
// source and transcript digest whose segments are exactly the given ranges.
func assertTimelineCheck(t *testing.T, response registry.ModelResponse, segments string) {
	t.Helper()
	if response.Decision.Kind != registry.DecisionToolCall || *response.Decision.ToolName != "run_checks" {
		t.Fatalf("Decision = %+v, want a run_checks call", response.Decision)
	}
	args := string(response.Decision.Arguments)
	for _, want := range []string{`"kind":"TIMELINE"`, `"sourceId":"` + videoSourceID + `"`, `"transcriptSha256":"` + videoTranscriptSHA + `"`, `"segments":` + segments} {
		if !strings.Contains(args, want) {
			t.Errorf("run_checks arguments %s lack %s", args, want)
		}
	}
}

func finalObject(t *testing.T, response registry.ModelResponse) map[string]any {
	t.Helper()
	var object map[string]any
	if response.Decision.Kind != registry.DecisionFinal || json.Unmarshal(response.Decision.Output, &object) != nil {
		t.Fatalf("Decision = %+v, want FINAL with a JSON object output", response.Decision)
	}
	return object
}

func assertDelivered(t *testing.T, response registry.ModelResponse, wantClip string) {
	t.Helper()
	object := finalObject(t, response)
	if object["outcome"] != "DELIVERED" || object["clipSha256"] != wantClip || object["cannotSatisfyReason"] != nil {
		t.Fatalf("FINAL = %v, want DELIVERED of clip %s with no cannotSatisfyReason", object, wantClip)
	}
	if reason, _ := object["selectionReason"].(string); reason == "" {
		t.Fatalf("FINAL = %v, want a selectionReason", object)
	}
}

// TestProvider_Generate_VideoEditPass_ReadsChecksRendersChecksAndDelivers plays the
// one-pass script: read the transcript, check a timeline while writing the plan into the
// State, render the checked digest, check the clip and declare delivery of that clip.
func TestProvider_Generate_VideoEditPass_ReadsChecksRendersChecksAndDelivers(t *testing.T) {
	const script = "video-edit-pass"

	read, err := generateVideoTurn(t, script)
	if err != nil {
		t.Fatalf("turn 0 Generate() error = %v", err)
	}
	assertToolCall(t, read, "read_material", videoReadArgs)
	if read.Decision.StatePatch != nil {
		t.Fatalf("turn 0 state patch = %s, want none before the plan is written", read.Decision.StatePatch)
	}

	check, err := generateVideoTurn(t, script, videoReadResult)
	if err != nil {
		t.Fatalf("turn 1 Generate() error = %v", err)
	}
	assertTimelineCheck(t, check, `[{"from":"S3","to":"S4"},{"from":"S5","to":"S5"},{"from":"S6","to":"S6"}]`)
	var state struct {
		Plan struct {
			Goal         string   `json:"goal"`
			Opening      string   `json:"opening"`
			MainSegments []string `json:"mainSegments"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(check.Decision.StatePatch, &state); err != nil || state.Plan.Goal == "" || state.Plan.Opening == "" || len(state.Plan.MainSegments) == 0 {
		t.Fatalf("turn 1 state patch = %s, want a plan with goal, opening and main segments", check.Decision.StatePatch)
	}

	render, err := generateVideoTurn(t, script, videoReadResult, timelineCheckResult(timelineA, true))
	if err != nil {
		t.Fatalf("turn 2 Generate() error = %v", err)
	}
	assertToolCall(t, render, "render_timeline", `{"timelineSha256":"`+timelineA+`"}`)

	clipCheck, err := generateVideoTurn(t, script, videoReadResult, timelineCheckResult(timelineA, true), renderResult(clipSHA, timelineA))
	if err != nil {
		t.Fatalf("turn 3 Generate() error = %v", err)
	}
	assertToolCall(t, clipCheck, "run_checks", `{"clipSha256":"`+clipSHA+`","kind":"CLIP"}`)

	final, err := generateVideoTurn(t, script, videoReadResult, timelineCheckResult(timelineA, true), renderResult(clipSHA, timelineA), clipCheckResult(clipSHA))
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	assertDelivered(t, final, clipSHA)
}

// TestProvider_Generate_VideoEditTooLong_FailedTimelineInsertsOneRevision plays the
// too-long script: the first timeline check fails on duration, the condition inserts a
// shorter timeline once, the render names the digest of the latest check, the clip check
// and the FINAL name the rendered clip, and a second failure would not insert again.
func TestProvider_Generate_VideoEditTooLong_FailedTimelineInsertsOneRevision(t *testing.T) {
	const script = "video-edit-too-long"
	first, err := generateVideoTurn(t, script, videoReadResult)
	if err != nil {
		t.Fatalf("turn 1 Generate() error = %v", err)
	}
	assertTimelineCheck(t, first, `[{"from":"S3","to":"S4"},{"from":"S5","to":"S5"},{"from":"S6","to":"S6"},{"from":"S11","to":"S13"}]`)

	results := []string{videoReadResult, timelineCheckResult("751de4decb5e2422dc42f51000924d2112020eac6d7b5eb344d827f17ec216ce", false)}
	inserted, err := generateVideoTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the failed check error = %v", err)
	}
	assertTimelineCheck(t, inserted, `[{"from":"S3","to":"S4"},{"from":"S5","to":"S5"},{"from":"S6","to":"S6"}]`)
	if !strings.Contains(string(inserted.Decision.StatePatch), `"mainSegments"`) {
		t.Fatalf("inserted state patch = %s, want the plan's main segments revised", inserted.Decision.StatePatch)
	}

	// The inserted check's result is not a scripted result: the next Turn is the script's
	// own render, of the latest check's digest.
	results = append(results, timelineCheckResult(timelineA, true))
	render, err := generateVideoTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the inserted check error = %v", err)
	}
	assertToolCall(t, render, "render_timeline", `{"timelineSha256":"`+timelineA+`"}`)

	results = append(results, renderResult(clipSHA, timelineA))
	clipCheck, err := generateVideoTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the render error = %v", err)
	}
	assertToolCall(t, clipCheck, "run_checks", `{"clipSha256":"`+clipSHA+`","kind":"CLIP"}`)

	final, err := generateVideoTurn(t, script, append(results, clipCheckResult(clipSHA))...)
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	assertDelivered(t, final, clipSHA)

	// A revised timeline that also fails is an ordinary result: the script goes on to its
	// render, which the Runtime, not the script, refuses for an unchecked digest.
	again, err := generateVideoTurn(t, script, videoReadResult, timelineCheckResult(timelineB, false), timelineCheckResult(timelineA, false))
	if err != nil {
		t.Fatalf("Generate() after a second failure error = %v", err)
	}
	assertToolCall(t, again, "render_timeline", `{"timelineSha256":"`+timelineA+`"}`)
}

// TestProvider_Generate_VideoEditTooLong_PassingTimelineSkipsTheRevision keeps the script
// linear when the first check passes: the render names that check's digest.
func TestProvider_Generate_VideoEditTooLong_PassingTimelineSkipsTheRevision(t *testing.T) {
	render, err := generateVideoTurn(t, "video-edit-too-long", videoReadResult, timelineCheckResult(timelineB, true))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	assertToolCall(t, render, "render_timeline", `{"timelineSha256":"`+timelineB+`"}`)
}

// TestProvider_Generate_VideoEditReviewFail_FailedReviewAddsTheFollowingSentence plays the
// reviewed script: the timeline passes its check, the review fails, the condition inserts a
// timeline that extends the last range by one sentence, the next review names the inserted
// check's artifact, the render names the passing review's timeline digest, and the FINAL
// names the rendered clip.
func TestProvider_Generate_VideoEditReviewFail_FailedReviewAddsTheFollowingSentence(t *testing.T) {
	const script = "video-edit-review-fail"
	first, err := generateVideoTurn(t, script, videoReadResult)
	if err != nil {
		t.Fatalf("turn 1 Generate() error = %v", err)
	}
	assertTimelineCheck(t, first, `[{"from":"S7","to":"S10"},{"from":"S22","to":"S23"},{"from":"S24","to":"S24"}]`)
	if strings.Contains(string(first.Decision.Arguments), "targetDurationMs") {
		t.Fatalf("run_checks arguments %s name a target duration the requirement does not give", first.Decision.Arguments)
	}

	results := []string{videoReadResult, timelineCheckResult(timelineA, true)}
	review, err := generateVideoTurn(t, script, results...)
	if err != nil {
		t.Fatalf("turn 2 Generate() error = %v", err)
	}
	assertToolCall(t, review, "review_artifact", `{"requirement":"Cut a 45-second vertical clip on why she loves engineering.","timelineArtifactId":"artifact_`+timelineA[:32]+`","transcriptArtifactId":"`+videoTranscriptArtifact+`"}`)

	results = append(results, contentReviewResult(timelineA, false))
	inserted, err := generateVideoTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the failed review error = %v", err)
	}
	assertTimelineCheck(t, inserted, `[{"from":"S7","to":"S10"},{"from":"S22","to":"S23"},{"from":"S24","to":"S25"}]`)

	results = append(results, timelineCheckResult(timelineB, true))
	reviewAgain, err := generateVideoTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the inserted check error = %v", err)
	}
	assertToolCall(t, reviewAgain, "review_artifact", `{"requirement":"Cut a 45-second vertical clip on why she loves engineering.","timelineArtifactId":"artifact_`+timelineB[:32]+`","transcriptArtifactId":"`+videoTranscriptArtifact+`"}`)

	results = append(results, contentReviewResult(timelineB, true))
	render, err := generateVideoTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the passing review error = %v", err)
	}
	assertToolCall(t, render, "render_timeline", `{"timelineSha256":"`+timelineB+`"}`)

	results = append(results, renderResult(clipSHA, timelineB))
	clipCheck, err := generateVideoTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the render error = %v", err)
	}
	assertToolCall(t, clipCheck, "run_checks", `{"clipSha256":"`+clipSHA+`","kind":"CLIP"}`)

	final, err := generateVideoTurn(t, script, append(results, clipCheckResult(clipSHA))...)
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	assertDelivered(t, final, clipSHA)
}

// TestProvider_Generate_VideoEditTrajectories_SameInputYieldsSameSequence proves
// determinism: every Turn of each video script, requested twice with the same request,
// returns the same Decision and state patch, conditions included.
func TestProvider_Generate_VideoEditTrajectories_SameInputYieldsSameSequence(t *testing.T) {
	cases := map[string][]string{
		"video-edit-pass":     {videoReadResult, timelineCheckResult(timelineA, true), renderResult(clipSHA, timelineA), clipCheckResult(clipSHA)},
		"video-edit-too-long": {videoReadResult, timelineCheckResult(timelineB, false), timelineCheckResult(timelineA, true), renderResult(clipSHA, timelineA), clipCheckResult(clipSHA)},
		"video-edit-review-fail": {videoReadResult, timelineCheckResult(timelineA, true), contentReviewResult(timelineA, false), timelineCheckResult(timelineB, true),
			contentReviewResult(timelineB, true), renderResult(clipSHA, timelineB), clipCheckResult(clipSHA)},
	}
	for script, results := range cases {
		t.Run(script, func(t *testing.T) {
			for n := 0; n <= len(results); n++ {
				first, err := generateVideoTurn(t, script, results[:n]...)
				if err != nil {
					t.Fatalf("turn %d Generate() error = %v", n, err)
				}
				second, err := generateVideoTurn(t, script, results[:n]...)
				if err != nil {
					t.Fatalf("turn %d second Generate() error = %v", n, err)
				}
				if first.Decision.Kind != second.Decision.Kind || string(first.Decision.Arguments) != string(second.Decision.Arguments) ||
					string(first.Decision.Output) != string(second.Decision.Output) || string(first.Decision.StatePatch) != string(second.Decision.StatePatch) {
					t.Fatalf("turn %d differs between two requests: %+v vs %+v", n, first.Decision, second.Decision)
				}
			}
		})
	}
}

// TestParseTrajectory_StatePatch_ObjectOnlyAndResolved covers the scripted state patch: a
// patch that is not a JSON object is rejected when the fixture is parsed, and a patch's
// placeholders are resolved like the arguments.
func TestParseTrajectory_StatePatch_ObjectOnlyAndResolved(t *testing.T) {
	for name, raw := range map[string]string{
		"tool call array patch": `{"turns":[{"toolCall":{"toolName":"t","arguments":{},"statePatch":[1]}},{"final":{"output":1}}]}`,
		"final string patch":    `{"turns":[{"final":{"output":1,"statePatch":"x"}}]}`,
		"condition array patch": `{"turns":[{"toolCall":{"toolName":"t","arguments":{}}},{"final":{"output":1}}],` +
			`"onToolResult":[{"when":{"tool":0,"pointer":"/passed","equals":false},"toolCall":{"toolName":"u","arguments":{},"statePatch":[]}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTrajectory([]byte(raw)); err == nil {
				t.Fatal("parseTrajectory() error = nil, want rejection")
			}
		})
	}

	trajectory, err := parseTrajectory([]byte(`{"turns":[{"toolCall":{"toolName":"t","arguments":{},"statePatch":{"plan":{"goal":"{{task:/requirement}}"}}}},{"final":{"output":1,"statePatch":{"done":true}}}]}`))
	if err != nil {
		t.Fatalf("parseTrajectory() error = %v", err)
	}
	request := scriptedRequest(t, "video-edit-pass")
	request.Messages[0] = userMessage(t, videoAssignment)
	resolver := placeholderResolver{request: request}
	scenario, err := resolver.scenario(trajectory.Turns[0].ToolCall, nil)
	if err != nil {
		t.Fatalf("scenario() error = %v", err)
	}
	if got := string(scenario.StatePatch); got != `{"plan":{"goal":"Cut a 45-second vertical clip on why she loves engineering."}}` {
		t.Fatalf("resolved state patch = %s", got)
	}
	final, err := resolver.scenario(nil, trajectory.Turns[1].Final)
	if err != nil {
		t.Fatalf("final scenario() error = %v", err)
	}
	if got := string(final.StatePatch); got != `{"done":true}` {
		t.Fatalf("final state patch = %s", got)
	}
}
