package runtime

import (
	"bytes"
	"encoding/json"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateRunInput validates a candidate Run.input against a Definition version's
// already-frozen runInputSchema (docs/06-execution-model.md §1.2: "创建 Run 时...用版本中
// 冻结的 runInputSchema 校验 input；它不能根据当前 Registry 生成另一份输入合同"). It never
// regenerates, mutates or re-derives frozenSchema; that generation happens once, inside
// Compile, at Save time.
//
// Caching choice: unlike the Compiler's per-Node-Type ConfigSchema cache,
// frozenSchema is not cached here. Run creation compiles it at most once per invocation
// (not a hot path repeated per Node Type across many Definitions), and a cache would need
// an eviction policy bounded by distinct Definition versions ever seen, which is
// complexity this stage does not need.
func ValidateRunInput(frozenSchema json.RawMessage, input json.RawMessage) []ValidationError {
	compiler := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(frozenSchema))
	if err != nil {
		return []ValidationError{{Code: CodeValidationFailed, Message: "frozen runInputSchema is not valid JSON: " + err.Error()}}
	}
	const schemaURL = "mem://run-input-schema"
	if err := compiler.AddResource(schemaURL, doc); err != nil {
		return []ValidationError{{Code: CodeValidationFailed, Message: "frozen runInputSchema is not a usable schema: " + err.Error()}}
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return []ValidationError{{Code: CodeValidationFailed, Message: "frozen runInputSchema is not a usable schema: " + err.Error()}}
	}

	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err != nil {
		return []ValidationError{{Code: CodeValidationFailed, Message: "input is not valid JSON: " + err.Error()}}
	}

	if verr := schema.Validate(instance); verr != nil {
		// "input" prefix keeps Path shaped like "input/document", consistent with the
		// ConfigSchema stage's "nodes[<id>].config" + instance-location convention.
		return schemaValidationErrors(verr, "input")
	}
	return nil
}
