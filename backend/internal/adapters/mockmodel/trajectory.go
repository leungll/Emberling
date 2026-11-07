package mockmodel

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// ScenarioScript selects a scripted multi-Turn trajectory: a fixture that lists, per Turn,
// either one Tool call or the FINAL output. It is reached through the text model's
// `script` config parameter or a "mock:script:<name>" directive in the last user message.
const ScenarioScript ScenarioKind = "script"

// trajectoryFiles holds the scripted trajectories, one JSON file per name. A file's base
// name without ".json" is the trajectory name.
//
//go:embed trajectories/*.json
var trajectoryFiles embed.FS

// Trajectory is one scripted Agent conversation. Turn N is answered from Turns[N], where N
// is the number of Tool results already present in the request. The Turn index is
// therefore a function of request content alone -- a recovered or replayed Turn sees the
// same committed Context and receives the same Decision -- never of hidden Provider state.
//
// Any string value in a Tool call's arguments or in a FINAL output that is exactly a
// placeholder is replaced by the JSON value it points at, keeping that value's type; a
// string that embeds placeholders, such as "advisories/{{task:/advisory}}.md", stays a
// string with each placeholder replaced by its value's text (a string as is, any other
// value as compact JSON). The placeholders are:
//   - "{{task:<json-pointer>}}" reads the Agent's task, the first user message, decoded
//     as JSON (a JSON string holding a JSON document, such as a media brief's text, is
//     decoded once more);
//   - "{{tool:<n>:<json-pointer>}}" reads the n-th Tool result (0-based) in the request;
//   - "{{latest:<json-pointer>}}" reads the most recent tool message in the request, be it
//     a scripted result, the result of an inserted step or a status message.
//
// A placeholder that does not resolve fails the Turn explicitly instead of sending a
// guessed value.
//
// OnToolStatus lets a script answer a Tool call the Runtime could not complete. A tool
// message whose top-level "status" names one of its conditions is not a Tool result of the
// script: it is left out of the Turn index and of the "{{tool:<n>:...}}" numbering, and
// when it is the latest tool message the condition's Decision is inserted instead of the
// next Turn. An inserted Tool call lets the script continue exactly as if the interrupted
// one had never been part of it; an inserted FINAL ends the script with a report instead,
// the way an Agent answers when the interrupted call cannot be verified or repeated. Each
// condition is inserted at most once per conversation: the same status arriving again
// fails the Turn explicitly, so a script can never loop on it. A REJECTED refusal, the
// message the Runtime hands back for a definite refusal such as SCOPE_DENIED or
// STALE_PATCH, is matched by its code as well, so one script can answer each refusal
// differently.
//
// OnToolResult lets a script branch on a Tool result the Runtime did complete, such as a
// verification whose "passed" member is false. A result condition names one scripted
// result by index and the member value it matches; when that result is the latest tool
// message and matches, the condition's Decision is inserted before the script's next Turn.
// The inserted step is extra work the script did not list, so its result is left out of
// the Turn index and of the "{{tool:<n>:...}}" numbering; the step after it reads what it
// needs through "{{latest:<json-pointer>}}", the most recent tool message whatever it is.
// A result condition is bound to one result index, so it fires at most once: the same
// member failing again on a later result is an ordinary result, and the script continues
// with its own Turns.
type Trajectory struct {
	Turns        []TrajectoryTurn            `json:"turns"`
	OnToolStatus []TrajectoryCondition       `json:"onToolStatus,omitempty"`
	OnToolResult []TrajectoryResultCondition `json:"onToolResult,omitempty"`
}

// TrajectoryCondition is one conditional Decision, inserted when the latest tool message
// carries Status, such as RESULT_UNCERTAIN or EXECUTION_INTERRUPTED: exactly one of a
// scripted Tool call or a FINAL. Code narrows a REJECTED status to one refusal code; it is
// allowed only with that status.
type TrajectoryCondition struct {
	Status   string              `json:"status"`
	Code     string              `json:"code,omitempty"`
	ToolCall *TrajectoryToolCall `json:"toolCall,omitempty"`
	Final    *TrajectoryFinal    `json:"final,omitempty"`
}

// key identifies the condition in the once-per-conversation accounting and in error text.
func (c TrajectoryCondition) key() string {
	if c.Code == "" {
		return c.Status
	}
	return c.Status + "/" + c.Code
}

// TrajectoryResultCondition is one conditional Decision inserted after a scripted result
// matches: exactly one of a scripted Tool call or a FINAL.
type TrajectoryResultCondition struct {
	When     *TrajectoryResultMatch `json:"when"`
	ToolCall *TrajectoryToolCall    `json:"toolCall,omitempty"`
	Final    *TrajectoryFinal       `json:"final,omitempty"`
}

// TrajectoryResultMatch names the scripted result a result condition inspects (Tool, the
// 0-based index in the "{{tool:<n>:...}}" numbering), the member to read (Pointer, an RFC
// 6901 JSON pointer) and the JSON value it must equal.
type TrajectoryResultMatch struct {
	Tool    int             `json:"tool"`
	Pointer string          `json:"pointer"`
	Equals  json.RawMessage `json:"equals"`
}

// TrajectoryTurn is one scripted Turn: exactly one of ToolCall or Final.
type TrajectoryTurn struct {
	ToolCall *TrajectoryToolCall `json:"toolCall,omitempty"`
	Final    *TrajectoryFinal    `json:"final,omitempty"`
}

// TrajectoryToolCall is a scripted TOOL_CALL Decision.
type TrajectoryToolCall struct {
	ToolName  string          `json:"toolName"`
	Arguments json.RawMessage `json:"arguments"`
}

// TrajectoryFinal is a scripted FINAL Decision: exactly one of Output, sent as the JSON
// value it resolves to, or OutputText, a JSON document whose placeholders are resolved and
// which is then sent as one JSON string. OutputText is how a script answers with a
// structured result through an Agent whose output is text, because a placeholder only
// ever replaces a whole string and so cannot be spliced into hand-written JSON text.
type TrajectoryFinal struct {
	Output     json.RawMessage `json:"output,omitempty"`
	OutputText json.RawMessage `json:"outputText,omitempty"`
}

// trajectories is the immutable set of embedded trajectories, loaded once at start-up. A
// malformed fixture is a build defect of this package, so it panics rather than letting a
// Run discover it.
var trajectories = mustLoadTrajectories()

func mustLoadTrajectories() map[string]Trajectory {
	loaded, err := loadTrajectories()
	if err != nil {
		panic("mockmodel: " + err.Error())
	}
	return loaded
}

func loadTrajectories() (map[string]Trajectory, error) {
	entries, err := trajectoryFiles.ReadDir("trajectories")
	if err != nil {
		return nil, fmt.Errorf("read trajectories: %w", err)
	}
	loaded := make(map[string]Trajectory, len(entries))
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		raw, err := trajectoryFiles.ReadFile(path.Join("trajectories", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read trajectory %s: %w", name, err)
		}
		trajectory, err := parseTrajectory(raw)
		if err != nil {
			return nil, fmt.Errorf("trajectory %s: %w", name, err)
		}
		loaded[name] = trajectory
	}
	return loaded, nil
}

// parseTrajectory decodes one fixture strictly and checks every Turn has exactly one
// shape and that the last Turn is FINAL, so a scripted Agent always terminates.
func parseTrajectory(raw []byte) (Trajectory, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var trajectory Trajectory
	if err := decoder.Decode(&trajectory); err != nil {
		return Trajectory{}, fmt.Errorf("decode: %w", err)
	}
	if len(trajectory.Turns) == 0 {
		return Trajectory{}, errors.New("has no turns")
	}
	for index, turn := range trajectory.Turns {
		switch {
		case (turn.ToolCall == nil) == (turn.Final == nil):
			return Trajectory{}, fmt.Errorf("turn %d must set exactly one of toolCall and final", index)
		case turn.ToolCall != nil && (turn.ToolCall.ToolName == "" || !json.Valid(turn.ToolCall.Arguments)):
			return Trajectory{}, fmt.Errorf("turn %d tool call needs a toolName and JSON arguments", index)
		case turn.Final != nil && (len(turn.Final.Output) == 0) == (len(turn.Final.OutputText) == 0):
			return Trajectory{}, fmt.Errorf("turn %d final must set exactly one of output and outputText", index)
		case turn.Final != nil && !json.Valid(turn.Final.Output) && !json.Valid(turn.Final.OutputText):
			return Trajectory{}, fmt.Errorf("turn %d final needs a JSON output", index)
		case turn.Final != nil && index != len(trajectory.Turns)-1:
			return Trajectory{}, fmt.Errorf("turn %d is final but is not the last turn", index)
		}
	}
	if trajectory.Turns[len(trajectory.Turns)-1].Final == nil {
		return Trajectory{}, errors.New("last turn is not final")
	}
	statuses := make(map[string]bool, len(trajectory.OnToolStatus))
	for index, condition := range trajectory.OnToolStatus {
		switch {
		case condition.Status == "":
			return Trajectory{}, fmt.Errorf("condition %d needs a status", index)
		case condition.Code != "" && condition.Status != rejectedStatus:
			return Trajectory{}, fmt.Errorf("condition %d names a code, which only a %s condition may", index, rejectedStatus)
		case statuses[condition.key()]:
			return Trajectory{}, fmt.Errorf("condition %d repeats status %s", index, condition.key())
		}
		if err := validateDecision(condition.ToolCall, condition.Final); err != nil {
			return Trajectory{}, fmt.Errorf("condition %d %w", index, err)
		}
		statuses[condition.key()] = true
	}
	matched := make(map[int]bool, len(trajectory.OnToolResult))
	for index, condition := range trajectory.OnToolResult {
		when := condition.When
		switch {
		case when == nil:
			return Trajectory{}, fmt.Errorf("result condition %d needs a when clause", index)
		case when.Tool < 0 || when.Tool >= len(trajectory.Turns) || trajectory.Turns[when.Tool].ToolCall == nil:
			return Trajectory{}, fmt.Errorf("result condition %d must name the index of a scripted tool call", index)
		case matched[when.Tool]:
			return Trajectory{}, fmt.Errorf("result condition %d repeats tool result %d", index, when.Tool)
		case when.Pointer != "" && !strings.HasPrefix(when.Pointer, "/"):
			return Trajectory{}, fmt.Errorf("result condition %d pointer must be empty or start with /", index)
		case len(when.Equals) == 0 || !json.Valid(when.Equals):
			return Trajectory{}, fmt.Errorf("result condition %d needs a JSON value to equal", index)
		}
		if err := validateDecision(condition.ToolCall, condition.Final); err != nil {
			return Trajectory{}, fmt.Errorf("result condition %d %w", index, err)
		}
		matched[when.Tool] = true
	}
	return trajectory, nil
}

// rejectedStatus is the status of the tool message the Runtime appends for a definite
// refusal handed back to the Agent; its code names the refusal.
const rejectedStatus = "REJECTED"

// validateDecision checks a conditional Decision has exactly one well-formed shape.
func validateDecision(toolCall *TrajectoryToolCall, final *TrajectoryFinal) error {
	switch {
	case (toolCall == nil) == (final == nil):
		return errors.New("must set exactly one of toolCall and final")
	case toolCall != nil && (toolCall.ToolName == "" || !json.Valid(toolCall.Arguments)):
		return errors.New("needs a tool call with a toolName and JSON arguments")
	case final != nil && (len(final.Output) == 0) == (len(final.OutputText) == 0):
		return errors.New("final must set exactly one of output and outputText")
	case final != nil && !json.Valid(final.Output) && !json.Valid(final.OutputText):
		return errors.New("final needs a JSON output")
	}
	return nil
}

// TrajectoryNames returns the embedded trajectory names, sorted.
func TrajectoryNames() []string {
	names := make([]string, 0, len(trajectories))
	for name := range trajectories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// configuredScript returns the `script` model config parameter, or "" when unset.
func configuredScript(request registry.ModelRequest) string {
	if len(request.ModelConfig) == 0 {
		return ""
	}
	var config struct {
		Script string `json:"script"`
	}
	if err := json.Unmarshal(request.ModelConfig, &config); err != nil {
		return ""
	}
	return config.Script
}

// scriptedScenario answers the request's current Turn from the named trajectory.
func scriptedScenario(request registry.ModelRequest, name string) (Scenario, error) {
	trajectory, ok := trajectories[name]
	if !ok {
		return Scenario{}, fmt.Errorf("script %q is not a known trajectory", name)
	}
	contents := toolMessageContents(request.Messages)
	toolResults, inserted, err := trajectory.scriptedResults(contents)
	if err != nil {
		return Scenario{}, fmt.Errorf("script %q: %w", name, err)
	}
	resolver := placeholderResolver{request: request, toolResults: toolResults}
	if len(contents) > 0 {
		resolver.latest = contents[len(contents)-1]
	}
	if inserted != nil {
		scenario, err := resolver.scenario(inserted.toolCall, inserted.final)
		if err != nil {
			return Scenario{}, fmt.Errorf("script %q condition %s: %w", name, inserted.label, err)
		}
		return scenario, nil
	}
	turnIndex := len(toolResults)
	if turnIndex >= len(trajectory.Turns) {
		return Scenario{}, fmt.Errorf("script %q has no turn %d", name, turnIndex)
	}
	turn := trajectory.Turns[turnIndex]
	scenario, err := resolver.scenario(turn.ToolCall, turn.Final)
	if err != nil {
		return Scenario{}, fmt.Errorf("script %q turn %d: %w", name, turnIndex, err)
	}
	return scenario, nil
}

// scenario renders one scripted Decision, a Tool call or a FINAL, with its placeholders
// resolved. parseTrajectory guarantees exactly one of the two is set.
func (r placeholderResolver) scenario(toolCall *TrajectoryToolCall, final *TrajectoryFinal) (Scenario, error) {
	if toolCall != nil {
		arguments, err := r.resolve(toolCall.Arguments)
		if err != nil {
			return Scenario{}, err
		}
		return Scenario{Kind: ScenarioToolCall, ToolName: toolCall.ToolName, ToolArguments: arguments}, nil
	}
	if len(final.OutputText) > 0 {
		document, err := r.resolve(final.OutputText)
		if err != nil {
			return Scenario{}, err
		}
		output, err := json.Marshal(string(document))
		if err != nil {
			return Scenario{}, fmt.Errorf("encode output text: %w", err)
		}
		return Scenario{Kind: ScenarioFinal, FinalOutput: output}, nil
	}
	output, err := r.resolve(final.Output)
	if err != nil {
		return Scenario{}, err
	}
	return Scenario{Kind: ScenarioFinal, FinalOutput: output}, nil
}

// insertedDecision is the conditional Decision the latest tool message triggers.
type insertedDecision struct {
	label    string
	toolCall *TrajectoryToolCall
	final    *TrajectoryFinal
}

// scriptedResults splits the request's tool messages into the script's own Tool results,
// oldest first, and the conditional Decision the latest tool message triggers, nil when
// the script continues with its own next Turn.
//
// A status condition's message is not a result and its inserted call's result stands in
// for the refused one. A result condition's matched message is a result; the inserted
// step's result, the message right after it, is not. A condition's status arriving more
// than once is refused.
func (t Trajectory) scriptedResults(contents []json.RawMessage) ([]json.RawMessage, *insertedDecision, error) {
	if len(t.OnToolStatus) == 0 && len(t.OnToolResult) == 0 {
		return contents, nil, nil
	}
	results := make([]json.RawMessage, 0, len(contents))
	seen := make(map[string]int, len(t.OnToolStatus))
	var inserted *insertedDecision
	// skipNext marks the next ordinary tool message as the result of a step a result
	// condition inserted, which the script does not count.
	skipNext := false
	for _, content := range contents {
		inserted = nil
		if condition := t.conditionFor(content); condition != nil {
			seen[condition.key()]++
			if seen[condition.key()] > 1 {
				return nil, nil, fmt.Errorf("tool status %s arrived %d times; its conditional decision is inserted once", condition.key(), seen[condition.key()])
			}
			inserted = &insertedDecision{label: condition.key(), toolCall: condition.ToolCall, final: condition.Final}
			continue
		}
		if skipNext {
			skipNext = false
			continue
		}
		results = append(results, content)
		if condition := t.resultConditionFor(len(results)-1, content); condition != nil {
			inserted = &insertedDecision{
				label:    fmt.Sprintf("result %d %s", condition.When.Tool, condition.When.Pointer),
				toolCall: condition.ToolCall, final: condition.Final,
			}
			skipNext = true
		}
	}
	return results, inserted, nil
}

// conditionFor returns the condition whose status the tool message content carries as its
// top-level "status" member, narrowed by its "code" member when the condition names one,
// or nil.
func (t Trajectory) conditionFor(content json.RawMessage) *TrajectoryCondition {
	document, err := decodeDocument(content)
	if err != nil {
		return nil
	}
	object, ok := document.(map[string]any)
	if !ok {
		return nil
	}
	status, ok := object["status"].(string)
	if !ok {
		return nil
	}
	code, _ := object["code"].(string)
	for index := range t.OnToolStatus {
		condition := &t.OnToolStatus[index]
		if condition.Status == status && (condition.Code == "" || condition.Code == code) {
			return condition
		}
	}
	return nil
}

// resultConditionFor returns the result condition bound to the scripted result at index
// whose member equals the condition's value, or nil.
func (t Trajectory) resultConditionFor(index int, content json.RawMessage) *TrajectoryResultCondition {
	for i := range t.OnToolResult {
		condition := &t.OnToolResult[i]
		if condition.When.Tool != index {
			continue
		}
		document, err := decodeDocument(content)
		if err != nil {
			return nil
		}
		value, err := resolvePointer(document, condition.When.Pointer)
		if err != nil {
			return nil
		}
		if jsonEqual(value, condition.When.Equals) {
			return condition
		}
		return nil
	}
	return nil
}

// jsonEqual reports whether a decoded JSON value and an encoded one denote the same JSON
// value, compared through one canonical encoding.
func jsonEqual(value any, raw json.RawMessage) bool {
	want, err := decodeNumbers(raw)
	if err != nil {
		return false
	}
	left, errLeft := json.Marshal(value)
	right, errRight := json.Marshal(want)
	return errLeft == nil && errRight == nil && bytes.Equal(left, right)
}

// toolMessageContents returns every "tool" role message's content, oldest first.
func toolMessageContents(messages []registry.ModelMessage) []json.RawMessage {
	var contents []json.RawMessage
	for _, message := range messages {
		if message.Role == roleTool {
			contents = append(contents, message.Content)
		}
	}
	return contents
}

// A placeholder's pointer cannot contain braces, so that several placeholders embedded in
// one string stay apart.
const placeholderSyntax = `\{\{(task|latest|tool:(\d+)):(/[^{}]*|)\}\}`

var (
	placeholderPattern         = regexp.MustCompile("^" + placeholderSyntax + "$")
	embeddedPlaceholderPattern = regexp.MustCompile(placeholderSyntax)
)

// placeholderResolver replaces placeholders in one scripted value.
type placeholderResolver struct {
	request     registry.ModelRequest
	toolResults []json.RawMessage
	// latest is the most recent tool message of the request, nil when there is none.
	latest json.RawMessage
}

func (r placeholderResolver) resolve(raw json.RawMessage) (json.RawMessage, error) {
	value, err := decodeNumbers(raw)
	if err != nil {
		return nil, errors.New("scripted value is not valid JSON")
	}
	resolved, err := r.walk(value)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(resolved); err != nil {
		return nil, fmt.Errorf("encode scripted value: %w", err)
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

func (r placeholderResolver) walk(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		// Members resolve in key order so the first unresolvable placeholder named in an
		// error is the same on every run; a Go map ranges in random order.
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			member := typed[key]
			resolved, err := r.walk(member)
			if err != nil {
				return nil, err
			}
			typed[key] = resolved
		}
		return typed, nil
	case []any:
		for index, item := range typed {
			resolved, err := r.walk(item)
			if err != nil {
				return nil, err
			}
			typed[index] = resolved
		}
		return typed, nil
	case string:
		return r.resolveString(typed)
	default:
		return value, nil
	}
}

// resolveString resolves the placeholders of one scripted string: a string that is exactly
// one placeholder becomes the value it points at, whatever its type; a string that embeds
// placeholders stays a string, each placeholder replaced by the text of its value.
func (r placeholderResolver) resolveString(text string) (any, error) {
	if match := placeholderPattern.FindStringSubmatch(text); match != nil {
		return r.lookup(match)
	}
	var failure error
	replaced := embeddedPlaceholderPattern.ReplaceAllStringFunc(text, func(placeholder string) string {
		if failure != nil {
			return placeholder
		}
		value, err := r.lookup(embeddedPlaceholderPattern.FindStringSubmatch(placeholder))
		if err != nil {
			failure = err
			return placeholder
		}
		if asText, ok := value.(string); ok {
			return asText
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			failure = fmt.Errorf("placeholder %s: encode value: %w", placeholder, err)
			return placeholder
		}
		return string(encoded)
	})
	if failure != nil {
		return nil, failure
	}
	return replaced, nil
}

// lookup reads the value one matched placeholder points at.
func (r placeholderResolver) lookup(match []string) (any, error) {
	source, err := r.source(match[1], match[2])
	if err != nil {
		return nil, err
	}
	resolved, err := resolvePointer(source, match[3])
	if err != nil {
		return nil, fmt.Errorf("placeholder %s: %w", match[0], err)
	}
	return resolved, nil
}

// source decodes the document a placeholder reads from.
func (r placeholderResolver) source(kind, toolIndex string) (any, error) {
	switch kind {
	case "task":
		for _, message := range r.request.Messages {
			if message.Role == "user" {
				return decodeDocument(message.Content)
			}
		}
		return nil, errors.New("request carries no task message")
	case "latest":
		if r.latest == nil {
			return nil, errors.New("request carries no tool message")
		}
		return decodeDocument(r.latest)
	}
	index, err := strconv.Atoi(toolIndex)
	if err != nil || index >= len(r.toolResults) {
		return nil, fmt.Errorf("request carries no tool result %s", toolIndex)
	}
	return decodeDocument(r.toolResults[index])
}

// decodeDocument decodes content, and decodes a JSON string once more when it holds a
// JSON object or array, the way a text port carries a structured document.
func decodeDocument(content json.RawMessage) (any, error) {
	value, err := decodeNumbers(content)
	if err != nil {
		return nil, errors.New("message content is not valid JSON")
	}
	if text, ok := value.(string); ok {
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if inner, err := decodeNumbers([]byte(trimmed)); err == nil {
				return inner, nil
			}
		}
	}
	return value, nil
}

func decodeNumbers(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("trailing content")
	}
	return value, nil
}

// resolvePointer applies an RFC 6901 JSON pointer to a decoded document.
func resolvePointer(document any, pointer string) (any, error) {
	if pointer == "" {
		return document, nil
	}
	current := document
	for _, token := range strings.Split(pointer[1:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			member, ok := node[token]
			if !ok {
				return nil, fmt.Errorf("no member %q", token)
			}
			current = member
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return nil, fmt.Errorf("no array element %q", token)
			}
			current = node[index]
		default:
			return nil, fmt.Errorf("cannot descend into %q", token)
		}
	}
	return current, nil
}
