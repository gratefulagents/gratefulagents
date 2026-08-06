package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
)

// minimalJSONSchema is a dependency-free validator for the JSON Schema subset
// used by task output schemas. Supported keywords:
//
//   - type: one of object, array, string, number, integer, boolean, null
//   - properties (object)
//   - required (object)
//   - items (single schema, arrays)
//   - enum
//   - additionalProperties (boolean form only)
//
// All other keywords (and non-boolean additionalProperties, type unions,
// $ref, allOf/anyOf/oneOf, pattern, format, min/max bounds, ...) are ignored:
// values pass validation for keywords this validator does not understand.
type minimalJSONSchema struct {
	raw map[string]any
}

// parseMinimalJSONSchema parses schemaJSON into a validator. The document
// must be a JSON object.
func parseMinimalJSONSchema(schemaJSON string) (*minimalJSONSchema, error) {
	var raw any
	if err := json.Unmarshal([]byte(schemaJSON), &raw); err != nil {
		return nil, fmt.Errorf("schema is not valid JSON: %w", err)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema must be a JSON object, got %s", jsonTypeName(raw))
	}
	return &minimalJSONSchema{raw: obj}, nil
}

// Validate checks a decoded JSON value (the encoding/json interface{}
// representation) against the schema and returns the first violation found.
func (s *minimalJSONSchema) Validate(value any) error {
	return validateJSONSubset(s.raw, value, "$")
}

func validateJSONSubset(schema map[string]any, value any, path string) error {
	if typ, ok := schema["type"].(string); ok {
		if err := checkJSONType(typ, value, path); err != nil {
			return err
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value is not one of the enum values", path)
		}
	}
	if obj, ok := value.(map[string]any); ok {
		if required, ok := schema["required"].([]any); ok {
			for _, entry := range required {
				name, ok := entry.(string)
				if !ok {
					continue
				}
				if _, present := obj[name]; !present {
					return fmt.Errorf("%s: missing required property %q", path, name)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, propValue := range obj {
			propSchema, hasSchema := properties[name].(map[string]any)
			if hasSchema {
				if err := validateJSONSubset(propSchema, propValue, path+"."+name); err != nil {
					return err
				}
				continue
			}
			if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
				return fmt.Errorf("%s: additional property %q is not allowed", path, name)
			}
		}
	}
	if arr, ok := value.([]any); ok {
		if items, ok := schema["items"].(map[string]any); ok {
			for i, element := range arr {
				if err := validateJSONSubset(items, element, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func checkJSONType(typ string, value any, path string) error {
	ok := false
	switch typ {
	case "object":
		_, ok = value.(map[string]any)
	case "array":
		_, ok = value.([]any)
	case "string":
		_, ok = value.(string)
	case "number":
		_, ok = value.(float64)
	case "integer":
		if n, isNumber := value.(float64); isNumber {
			ok = n == math.Trunc(n)
		}
	case "boolean":
		_, ok = value.(bool)
	case "null":
		ok = value == nil
	default:
		// Unknown type names are ignored like any other unsupported keyword.
		return nil
	}
	if !ok {
		return fmt.Errorf("%s: expected %s, got %s", path, typ, jsonTypeName(value))
	}
	return nil
}

func jsonTypeName(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	}
	return "unknown"
}
