package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// validateConfigSchema is the Compiler's second stage: every node's config must satisfy
// its Node Type's registered ConfigSchema (each node config is checked against
// ConfigSchema first). Nodes whose type could not be resolved were already
// reported by the Structure stage and Compile never reaches this stage in that case, so
// they are silently skipped here rather than double-reported.
func (c *Compiler) validateConfigSchema(def domain.Definition) []ValidationError {
	var errs []ValidationError
	for _, node := range def.Nodes {
		meta, ok := c.catalog.NodeMetadata(node.Type)
		if !ok {
			continue
		}
		schema, err := c.compiledConfigSchema(node.Type, meta.ConfigSchema)
		if err != nil {
			errs = append(errs, ValidationError{
				Code:    CodeValidationFailed,
				NodeID:  node.ID,
				Path:    "nodes[" + node.ID + "].config",
				Message: fmt.Sprintf("configSchema for node type %q is not usable: %v", node.Type, err),
			})
			continue
		}

		raw := node.Config
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			errs = append(errs, ValidationError{
				Code:    CodeValidationFailed,
				NodeID:  node.ID,
				Path:    "nodes[" + node.ID + "].config",
				Message: "config is not valid JSON: " + err.Error(),
			})
			continue
		}

		if verr := schema.Validate(instance); verr != nil {
			for _, ve := range schemaValidationErrors(verr, "nodes["+node.ID+"].config") {
				ve.NodeID = node.ID
				errs = append(errs, ve)
			}
		}
	}
	return errs
}

// compiledConfigSchema returns the cached compiled JSON Schema for nodeType, compiling
// and caching it on first use. Each node type gets its own *jsonschema.Compiler instance
// so synthetic resource URLs never collide across concurrent compiles of different node
// types; the cache itself is guarded by Compiler.mu.
func (c *Compiler) compiledConfigSchema(nodeType string, raw json.RawMessage) (*jsonschema.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if schema, ok := c.schemaCache[nodeType]; ok {
		return schema, nil
	}

	compiler := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	url := "mem://node-types/" + nodeType
	if err := compiler.AddResource(url, doc); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile(url)
	if err != nil {
		return nil, err
	}
	c.schemaCache[nodeType] = schema
	return schema, nil
}

// schemaValidationErrors flattens a *jsonschema.ValidationError tree (returned by
// Schema.Validate) into a flat, deterministically-orderable list of ValidationError,
// prefixing each leaf's JSON-pointer instance location with basePath.
func schemaValidationErrors(err error, basePath string) []ValidationError {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return []ValidationError{{Code: CodeValidationFailed, Message: err.Error(), Path: basePath}}
	}
	out := verr.BasicOutput()
	leaves := out.Errors
	if len(leaves) == 0 {
		leaves = []jsonschema.OutputUnit{*out}
	}
	result := make([]ValidationError, 0, len(leaves))
	for _, leaf := range leaves {
		msg := ""
		if leaf.Error != nil {
			msg = leaf.Error.String()
		}
		result = append(result, ValidationError{
			Code:    CodeValidationFailed,
			Path:    basePath + leaf.InstanceLocation,
			Message: msg,
		})
	}
	return result
}
