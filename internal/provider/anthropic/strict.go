package anthropic

import (
	"encoding/json"
	"strings"
)

// maxStrictTools is the documented ceiling on strict tools in one request.
// Beyond it the API rejects the whole call, so the adapter stops marking tools
// strict rather than losing an agent turn.
const maxStrictTools = 20

// strictFormats is the set of string formats constrained decoding accepts. A
// schema asking for anything else is kept non-strict: the format carries real
// meaning to the model, so dropping it silently would be worse than losing the
// guarantee.
var strictFormats = map[string]bool{
	"date-time": true,
	"time":      true,
	"date":      true,
	"duration":  true,
	"email":     true,
	"hostname":  true,
	"uri":       true,
	"ipv4":      true,
	"ipv6":      true,
	"uuid":      true,
}

// strictRejected lists the keywords that make the API answer 400 for the whole
// request once strict is on. pattern is absent on purpose: constrained decoding
// accepts a subset of regular expressions, and only the exotic half is
// documented as failing.
var strictRejected = map[string]bool{
	"minimum":               true,
	"maximum":               true,
	"exclusiveMinimum":      true,
	"exclusiveMaximum":      true,
	"multipleOf":            true,
	"minLength":             true,
	"maxLength":             true,
	"maxItems":              true,
	"uniqueItems":           true,
	"minContains":           true,
	"maxContains":           true,
	"minProperties":         true,
	"maxProperties":         true,
	"oneOf":                 true,
	"not":                   true,
	"if":                    true,
	"then":                  true,
	"else":                  true,
	"propertyNames":         true,
	"patternProperties":     true,
	"dependentSchemas":      true,
	"dependencies":          true,
	"unevaluatedProperties": true,
	"prefixItems":           true,
	"contains":              true,

	// References are documented as supported, but resolving one needs the whole
	// document and recursive schemas are refused. Rather than guess, a schema
	// carrying a reference stays non-strict.
	"$ref":        true,
	"$defs":       true,
	"definitions": true,
}

// strictSchema rewrites a tool schema into the shape strict mode validates, or
// reports that it cannot. The only rewrite is additionalProperties:false on
// every object, which is what the API requires and what a tool schema written
// for prose tends to leave out. Optional properties stay optional: the API
// accepts them, it only budgets how many may appear in one request.
func strictSchema(schema json.RawMessage) (json.RawMessage, bool) {
	rewritten, ok := strictNode(schema)
	if !ok {
		return schema, false
	}
	return rewritten, true
}

// strictNode rewrites one schema node and reports whether the result is
// something constrained decoding accepts.
func strictNode(raw json.RawMessage) (json.RawMessage, bool) {
	fields, ok := schemaFields(raw)
	if !ok {
		return nil, false
	}
	for name, value := range fields {
		if !strictKeyword(name, value) {
			return nil, false
		}
	}

	if properties, present := fields["properties"]; present {
		children, ok := schemaFields(properties)
		if !ok {
			return nil, false
		}
		for name, child := range children {
			rewritten, ok := strictNode(child)
			if !ok {
				return nil, false
			}
			children[name] = rewritten
		}
		encoded, err := json.Marshal(children)
		if err != nil {
			return nil, false
		}
		fields["properties"] = encoded
	}

	if items, present := fields["items"]; present {
		// A tuple form (an array of schemas) is refused by the API.
		rewritten, ok := strictNode(items)
		if !ok {
			return nil, false
		}
		fields["items"] = rewritten
	}

	// allOf and anyOf are documented as accepted; their members are not.
	for _, name := range []string{"allOf", "anyOf"} {
		variants, present := fields[name]
		if !present {
			continue
		}
		var members []json.RawMessage
		if err := json.Unmarshal(variants, &members); err != nil || len(members) == 0 {
			return nil, false
		}
		for i, member := range members {
			rewritten, ok := strictNode(member)
			if !ok {
				return nil, false
			}
			members[i] = rewritten
		}
		encoded, err := json.Marshal(members)
		if err != nil {
			return nil, false
		}
		fields[name] = encoded
	}

	if schemaIsObject(fields) {
		if declared, present := fields["additionalProperties"]; present {
			// Anything but literal false is refused, including a schema.
			if strings.TrimSpace(string(declared)) != "false" {
				return nil, false
			}
		} else {
			fields["additionalProperties"] = json.RawMessage("false")
		}
	}

	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// strictKeyword reports whether a single keyword and its value survive strict
// mode. The boolean keywords it accepts are the ones the API documents as
// supported and the adapter leaves untouched.
func strictKeyword(name string, value json.RawMessage) bool {
	if strictRejected[name] {
		return false
	}
	switch name {
	case "minItems":
		// Constrained decoding understands an empty or a single element and
		// nothing in between.
		text := strings.TrimSpace(string(value))
		return text == "0" || text == "1"
	case "format":
		var format string
		return json.Unmarshal(value, &format) == nil && strictFormats[format]
	}
	return true
}

// schemaIsObject reports whether a node describes an object, which is where
// additionalProperties has to be pinned.
func schemaIsObject(fields map[string]json.RawMessage) bool {
	if declared, present := fields["type"]; present {
		var name string
		if json.Unmarshal(declared, &name) == nil {
			return name == "object"
		}
	}
	_, present := fields["properties"]
	return present
}

// schemaFields decodes a JSON document that has to be an object of member
// names. Numbers keep their literal text because a schema's bounds are read as
// text, not as values the adapter would re-encode.
func schemaFields(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if stripped := stripEmpty(raw); stripped == nil {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

// stripEmpty returns the trimmed document, or nil when there is nothing to
// read. A boolean schema ("true"/"false") reaches the decoder as a document
// that is not an object and fails there.
func stripEmpty(raw json.RawMessage) []byte {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return []byte(trimmed)
}
