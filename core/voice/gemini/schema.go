package gemini

// JSON Schema (what tools declare) → Gemini's Schema, an OpenAPI 3.0 subset. Port of
// voice-gemini/src/schema.rs.
//
// Gemini accepts only these fields (https://ai.google.dev/api/generate-content, Schema):
// type, format, title, description, nullable, enum, maxItems, minItems, properties, required,
// minProperties, maxProperties, minLength, maxLength, pattern, example, anyOf,
// propertyOrdering, default, items, minimum, maximum. Types are the Type enum (STRING,
// NUMBER, INTEGER, BOOLEAN, ARRAY, OBJECT, NULL). Everything else (additionalProperties,
// $schema, $ref, const, oneOf, …) is dropped or rewritten.

import (
	"bytes"
	"encoding/json"
)

// plainFields are copied unchanged when present.
var plainFields = []string{
	"title", "description", "nullable", "maxItems", "minItems", "minProperties", "maxProperties",
	"minLength", "maxLength", "pattern", "example", "default", "minimum", "maximum", "propertyOrdering",
}

func geminiType(t string) string {
	switch t {
	case "string", "number", "integer", "boolean", "array", "object", "null":
		return string(bytes.ToUpper([]byte(t)))
	}
	return ""
}

// formatAllowed: formats Gemini documents per type; others are dropped rather than risk a
// rejected setup.
func formatAllowed(ty, format string) bool {
	switch ty {
	case "STRING":
		return format == "enum" || format == "date-time"
	case "NUMBER":
		return format == "float" || format == "double"
	case "INTEGER":
		return format == "int32" || format == "int64"
	}
	return false
}

// ToGemini converts one decoded JSON Schema node. Non-object input (e.g. true) becomes an
// empty schema.
func ToGemini(schema any) any {
	src, ok := schema.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}

	// type: a string, or a list where "null" means nullable.
	ty := ""
	switch t := src["type"].(type) {
	case string:
		ty = geminiType(t)
	case []any:
		var kinds []string
		nullable := false
		for _, k := range t {
			s, _ := k.(string)
			switch g := geminiType(s); g {
			case "":
			case "NULL":
				nullable = true
			default:
				kinds = append(kinds, g)
			}
		}
		if nullable {
			out["nullable"] = true
		}
		switch len(kinds) {
		case 0:
		case 1:
			ty = kinds[0]
		default:
			alts := make([]any, len(kinds))
			for i, k := range kinds {
				alts[i] = map[string]any{"type": k}
			}
			out["anyOf"] = alts
		}
	}
	if _, has := src["properties"]; ty == "" && has {
		ty = "OBJECT"
	}
	if ty != "" {
		out["type"] = ty
	}

	for _, k := range plainFields {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	if f, ok := src["format"].(string); ok && formatAllowed(ty, f) {
		out["format"] = f
	}

	// enum (strings only in Gemini); const is a one-value enum.
	var values []any
	hasValues := false
	if e, ok := src["enum"].([]any); ok {
		values, hasValues = e, true
	} else if c, ok := src["const"]; ok {
		values, hasValues = []any{c}, true
	}
	if hasValues && len(values) > 0 {
		allStrings := true
		for _, v := range values {
			if _, ok := v.(string); !ok {
				allStrings = false
			}
		}
		if allStrings {
			out["enum"] = values
			if ty == "" {
				out["type"] = "STRING"
			}
		}
	}

	if props, ok := src["properties"].(map[string]any); ok {
		converted := make(map[string]any, len(props))
		for k, v := range props {
			converted[k] = ToGemini(v)
		}
		if req, ok := src["required"].([]any); ok {
			var kept []any
			for _, r := range req {
				if name, ok := r.(string); ok {
					if _, exists := converted[name]; exists {
						kept = append(kept, r)
					}
				}
			}
			if len(kept) > 0 {
				out["required"] = kept
			}
		}
		out["properties"] = converted
	}

	switch items := src["items"].(type) {
	case []any:
		if len(items) > 0 {
			out["items"] = ToGemini(items[0])
		}
	case map[string]any:
		out["items"] = ToGemini(items)
	}

	alts, ok := src["anyOf"].([]any)
	if _, has := src["anyOf"]; !has {
		alts, ok = src["oneOf"].([]any)
	}
	if ok {
		conv := make([]any, len(alts))
		for i, a := range alts {
			conv[i] = ToGemini(a)
		}
		out["anyOf"] = conv
	}
	return out
}

// Parameters is the parameters of a function declaration, or ok=false when the tool takes no
// arguments: Gemini rejects an OBJECT schema whose properties is empty.
func Parameters(schema json.RawMessage) (map[string]any, bool) {
	var v any
	d := json.NewDecoder(bytes.NewReader(schema))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, false
	}
	converted := ToGemini(v).(map[string]any)
	props, _ := converted["properties"].(map[string]any)
	if len(props) == 0 {
		return nil, false
	}
	return converted, true
}
