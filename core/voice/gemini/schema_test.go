package gemini

// Port of ai-mascot crates/voice-gemini/src/schema.rs tests (6/6).

import (
	"encoding/json"
	"reflect"
	"testing"
)

func parse(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad fixture %s: %v", s, err)
	}
	return v
}

// jsonEq compares two values by their JSON meaning.
func jsonEq(t *testing.T, got any, want string, msg ...any) {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parse(t, string(b)), parse(t, want)) {
		t.Fatalf("%v\n got: %s\nwant: %s", msg, b, want)
	}
}

func TestStripsAdditionalPropertiesAndUppercasesTypes(t *testing.T) {
	delegate := parse(t, `{
		"type": "object",
		"properties": {
			"task": { "type": "string", "description": "What the user wants done." },
			"context": { "type": "string" }
		},
		"required": ["task"],
		"additionalProperties": false
	}`)
	jsonEq(t, ToGemini(delegate), `{
		"type": "OBJECT",
		"properties": {
			"task": { "type": "STRING", "description": "What the user wants done." },
			"context": { "type": "STRING" }
		},
		"required": ["task"]
	}`)
}

func TestDropsUnsupportedKeywordsEverywhere(t *testing.T) {
	s := parse(t, `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"$id": "x", "type": "object", "title": "T",
		"properties": {
			"n": { "type": "integer", "minimum": 1, "maximum": 9, "exclusiveMaximum": 10, "multipleOf": 2 },
			"nested": { "type": "object", "properties": { "a": { "type": "boolean" } }, "additionalProperties": { "type": "string" } }
		},
		"patternProperties": { "^x": {} },
		"$defs": { "y": {} }
	}`)
	jsonEq(t, ToGemini(s), `{
		"type": "OBJECT", "title": "T",
		"properties": {
			"n": { "type": "INTEGER", "minimum": 1, "maximum": 9 },
			"nested": { "type": "OBJECT", "properties": { "a": { "type": "BOOLEAN" } } }
		}
	}`)
}

func TestEnumsConstAndNullable(t *testing.T) {
	jsonEq(t, ToGemini(parse(t, `{ "type": "string", "enum": ["claude", "codex"] }`)), `{ "type": "STRING", "enum": ["claude", "codex"] }`)
	jsonEq(t, ToGemini(parse(t, `{ "const": "yes" }`)), `{ "type": "STRING", "enum": ["yes"] }`)
	jsonEq(t, ToGemini(parse(t, `{ "type": "integer", "enum": [1, 2] }`)), `{ "type": "INTEGER" }`, "non-string enums are dropped")
	jsonEq(t, ToGemini(parse(t, `{ "type": ["string", "null"] }`)), `{ "type": "STRING", "nullable": true }`)
	jsonEq(t, ToGemini(parse(t, `{ "type": ["string", "integer"] }`)), `{ "anyOf": [{ "type": "STRING" }, { "type": "INTEGER" }] }`)
}

func TestArraysAnyOfAndFormats(t *testing.T) {
	jsonEq(t, ToGemini(parse(t, `{ "type": "array", "items": { "type": "string", "format": "uri" }, "maxItems": 3, "uniqueItems": true }`)),
		`{ "type": "ARRAY", "items": { "type": "STRING" }, "maxItems": 3 }`, "uri is not a Gemini format")
	if got := ToGemini(parse(t, `{ "type": "string", "format": "date-time" }`)).(map[string]any)["format"]; got != "date-time" {
		t.Fatal(got)
	}
	jsonEq(t, ToGemini(parse(t, `{ "oneOf": [{ "type": "string" }, { "type": "number", "format": "double" }] }`)),
		`{ "anyOf": [{ "type": "STRING" }, { "type": "NUMBER", "format": "double" }] }`)
}

func TestRequiredNamesMustExistAndUntypedPropertiesMeanObject(t *testing.T) {
	jsonEq(t, ToGemini(parse(t, `{ "properties": { "a": { "type": "string" } }, "required": ["a", "ghost"] }`)),
		`{ "type": "OBJECT", "properties": { "a": { "type": "STRING" } }, "required": ["a"] }`)
	jsonEq(t, ToGemini(true), `{}`)
}

func TestNoArgumentToolsHaveNoParameters(t *testing.T) {
	if p, ok := Parameters(json.RawMessage(`{ "type": "object", "properties": {}, "additionalProperties": false }`)); ok {
		t.Fatal(p)
	}
	if p, ok := Parameters(json.RawMessage(`{ "type": "object" }`)); ok {
		t.Fatal(p)
	}
	p, ok := Parameters(json.RawMessage(`{ "type": "object", "properties": { "job_id": { "type": "string" } } }`))
	if !ok {
		t.Fatal("no parameters")
	}
	jsonEq(t, p, `{ "type": "OBJECT", "properties": { "job_id": { "type": "STRING" } } }`)
}
