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
// placeholder is replaced by the JSON value it points at, keeping that value's type:
//   - "{{task:<json-pointer>}}" reads the Agent's task, the first user message, decoded
//     as JSON (a JSON string holding a JSON document, such as a media brief's text, is
//     decoded once more);
//   - "{{tool:<n>:<json-pointer>}}" reads the n-th Tool result (0-based) in the request.
//
// A placeholder that does not resolve fails the Turn explicitly instead of sending a
// guessed value.
//
// OnToolStatus lets a script answer a Tool call the Runtime could not complete. A tool
// message whose top-level "status" names one of its conditions is not a Tool result of the
// script: it is left out of the Turn index and of the "{{tool:<n>:...}}" numbering, and
// when it is the latest tool message the condition's Decision is inserted instead of the
// next Turn. A script therefore continues after the inserted call exactly as if the
// interrupted one had never been part of it. Each condition is inserted at most once per
// conversation: the same status arriving again fails the Turn explicitly, so a script can
// never loop on it.
type Trajectory struct {
	Turns        []TrajectoryTurn      `json:"turns"`
	OnToolStatus []TrajectoryCondition `json:"onToolStatus,omitempty"`
}

// TrajectoryCondition is one conditional Decision: the scripted Tool call inserted when the
// latest tool message carries Status, such as RESULT_UNCERTAIN or EXECUTION_INTERRUPTED.
type TrajectoryCondition struct {
	Status   string              `json:"status"`
	ToolCall *TrajectoryToolCall `json:"toolCall"`
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
		case statuses[condition.Status]:
			return Trajectory{}, fmt.Errorf("condition %d repeats status %s", index, condition.Status)
		case condition.ToolCall == nil || condition.ToolCall.ToolName == "" || !json.Valid(condition.ToolCall.Arguments):
			return Trajectory{}, fmt.Errorf("condition %d needs a tool call with a toolName and JSON arguments", index)
		}
		statuses[condition.Status] = true
	}
	return trajectory, nil
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
	toolResults, latest, err := trajectory.scriptedResults(toolMessageContents(request.Messages))
	if err != nil {
		return Scenario{}, fmt.Errorf("script %q: %w", name, err)
	}
	resolver := placeholderResolver{request: request, toolResults: toolResults}
	if latest != nil {
		arguments, err := resolver.resolve(latest.ToolCall.Arguments)
		if err != nil {
			return Scenario{}, fmt.Errorf("script %q condition %s: %w", name, latest.Status, err)
		}
		return Scenario{Kind: ScenarioToolCall, ToolName: latest.ToolCall.ToolName, ToolArguments: arguments}, nil
	}
	turnIndex := len(toolResults)
	if turnIndex >= len(trajectory.Turns) {
		return Scenario{}, fmt.Errorf("script %q has no turn %d", name, turnIndex)
	}
	turn := trajectory.Turns[turnIndex]
	if turn.ToolCall != nil {
		arguments, err := resolver.resolve(turn.ToolCall.Arguments)
		if err != nil {
			return Scenario{}, fmt.Errorf("script %q turn %d: %w", name, turnIndex, err)
		}
		return Scenario{Kind: ScenarioToolCall, ToolName: turn.ToolCall.ToolName, ToolArguments: arguments}, nil
	}
	if len(turn.Final.OutputText) > 0 {
		document, err := resolver.resolve(turn.Final.OutputText)
		if err != nil {
			return Scenario{}, fmt.Errorf("script %q turn %d: %w", name, turnIndex, err)
		}
		output, err := json.Marshal(string(document))
		if err != nil {
			return Scenario{}, fmt.Errorf("script %q turn %d: encode output text: %w", name, turnIndex, err)
		}
		return Scenario{Kind: ScenarioFinal, FinalOutput: output}, nil
	}
	output, err := resolver.resolve(turn.Final.Output)
	if err != nil {
		return Scenario{}, fmt.Errorf("script %q turn %d: %w", name, turnIndex, err)
	}
	return Scenario{Kind: ScenarioFinal, FinalOutput: output}, nil
}

// scriptedResults splits the request's tool messages into the script's own Tool results,
// oldest first, and the condition the latest tool message triggers, nil when it is an
// ordinary result. A condition's status arriving more than once is refused.
func (t Trajectory) scriptedResults(contents []json.RawMessage) ([]json.RawMessage, *TrajectoryCondition, error) {
	if len(t.OnToolStatus) == 0 {
		return contents, nil, nil
	}
	results := make([]json.RawMessage, 0, len(contents))
	seen := make(map[string]int, len(t.OnToolStatus))
	var latest *TrajectoryCondition
	for _, content := range contents {
		latest = t.conditionFor(content)
		if latest == nil {
			results = append(results, content)
			continue
		}
		seen[latest.Status]++
		if seen[latest.Status] > 1 {
			return nil, nil, fmt.Errorf("tool status %s arrived %d times; its conditional decision is inserted once", latest.Status, seen[latest.Status])
		}
	}
	return results, latest, nil
}

// conditionFor returns the condition whose status the tool message content carries as its
// top-level "status" member, or nil.
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
	for index := range t.OnToolStatus {
		if t.OnToolStatus[index].Status == status {
			return &t.OnToolStatus[index]
		}
	}
	return nil
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

var placeholderPattern = regexp.MustCompile(`^\{\{(task|tool:(\d+)):(/.*|)\}\}$`)

// placeholderResolver replaces placeholders in one scripted value.
type placeholderResolver struct {
	request     registry.ModelRequest
	toolResults []json.RawMessage
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
		for key, member := range typed {
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
		match := placeholderPattern.FindStringSubmatch(typed)
		if match == nil {
			return typed, nil
		}
		source, err := r.source(match[1], match[2])
		if err != nil {
			return nil, err
		}
		resolved, err := resolvePointer(source, match[3])
		if err != nil {
			return nil, fmt.Errorf("placeholder %s: %w", typed, err)
		}
		return resolved, nil
	default:
		return value, nil
	}
}

// source decodes the document a placeholder reads from.
func (r placeholderResolver) source(kind, toolIndex string) (any, error) {
	if kind == "task" {
		for _, message := range r.request.Messages {
			if message.Role == "user" {
				return decodeDocument(message.Content)
			}
		}
		return nil, errors.New("request carries no task message")
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
