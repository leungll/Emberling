package mockprovider

import "encoding/json"

// fixedTaskCost is the cost every successful task result reports under test controls. It
// is one constant rather than a price model: it exists so a caller's cost recording and
// totals can be checked against a known value. Like an OpenRouter response's `usage.cost`,
// it is a plain number in US dollars with no currency field of its own.
const fixedTaskCost = "0.04"

// fixedUsage is the `usage` member withFixedCost adds.
var fixedUsage = json.RawMessage(`{"cost":` + fixedTaskCost + `}`)

// withFixedCost adds `usage: {cost: fixedTaskCost}` to a task result payload that is a
// JSON object without a `usage` member of its own. A caller-supplied usage, or a payload
// that is not an object, is returned unchanged, so an explicit payload still reaches the
// callback as the caller wrote it.
func withFixedCost(payload json.RawMessage) json.RawMessage {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(payload, &members); err != nil || members == nil {
		return payload
	}
	if _, present := members["usage"]; present {
		return payload
	}
	members["usage"] = fixedUsage
	encoded, err := json.Marshal(members)
	if err != nil {
		return payload
	}
	return encoded
}
