// Package schema implements the closed-vocabulary structural JSON Schema
// compiler and validator used by cli-registry.
//
// It supports exactly the umpire-json-schema profile v1 valueSchema subset:
// object/array/scalar types with properties, required, additionalProperties:false,
// items, min/maxItems, min/maxLength, min/maximum, exclusive bounds, enum,
// const, local acyclic $defs + #/$defs/... references, and tagged oneOf unions.
// Anything else fails compilation — never silently ignored.
//
// This is the structural authority only. Umpire availability semantics
// (conditions, fairness, reasons) are intentionally out of scope.
package schema

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

// StructuralIssue is a normalized structural validation finding. Normative
// cross-runtime fields are Code and Path (RFC 6901 pointer into the value);
// Source is always "json-schema". Messages are not normative.
type StructuralIssue struct {
	Source  string
	Code    string
	Path    string
	Message string
}

// DefinitionIssue is a schema-definition problem (unsupported keyword, bad or
// cyclic reference, invalid tagged union).
type DefinitionIssue struct {
	Code    string
	Path    string // RFC 6901 pointer into the schema document
	Message string
}

// CompileError aggregates definition issues discovered while compiling a schema.
type CompileError struct {
	Issues []DefinitionIssue
}

func (e *CompileError) Error() string {
	if len(e.Issues) == 0 {
		return "schema compilation failed"
	}
	return fmt.Sprintf("schema compilation failed: %d issue(s)", len(e.Issues))
}

// ValueSchema is a compiled, immutable schema ready for validation.
type ValueSchema struct {
	root *node
}

// Compile builds a ValueSchema from a raw JSON Schema (as decoded from JSON,
// e.g. map[string]any). Unsupported vocabulary, invalid references, and cyclic
// references are rejected. A nil schema parses as a compile error (closed vocab
// requires an explicit schema).
func Compile(raw any) (*ValueSchema, error) {
	defs, err := defsOf(raw)
	if err != nil {
		return nil, err
	}
	n, issues := compileNode(raw, defs, map[string]bool{}, "")
	if len(issues) > 0 {
		return nil, &CompileError{Issues: issues}
	}
	return &ValueSchema{root: n}, nil
}

// knownRootKeys are the recognized vocabulary keywords. Anything else is an
// unsupported keyword. $schema, title, description, and annotations are
// allowed but ignored.
var allowedKeys = map[string]bool{
	"$schema": true, "$defs": true, "$ref": true,
	"type": true, "properties": true, "required": true, "additionalProperties": true,
	"items": true, "minItems": true, "maxItems": true,
	"minLength": true, "maxLength": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"enum": true, "const": true, "oneOf": true,
	"title": true, "description": true,
}

func defsOf(raw any) (map[string]any, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, &CompileError{Issues: []DefinitionIssue{{Code: "invalidProfile", Path: "", Message: "schema must be a JSON object"}}}
	}
	defs, _ := m["$defs"].(map[string]any)
	return defs, nil
}

type nodeKind int

const (
	kindAny nodeKind = iota
	kindString
	kindNumber
	kindInteger
	kindBoolean
	kindArray
	kindObject
	kindOneOf
	kindValue // const/enum without an explicit type
)

type node struct {
	kind nodeKind

	enum     []any
	hasConst bool
	constVal any
	minLen   *int
	maxLen   *int

	min   *float64
	max   *float64
	exMin *float64
	exMax *float64

	items    *node
	minItems *int
	maxItems *int

	properties map[string]*node
	required   []string

	discKey  string // tagged oneOf discriminator property
	branches []*node
}

func intp(v float64) *int { i := int(v); return &i }

func compileNode(raw any, defs map[string]any, visiting map[string]bool, spath string) (*node, []DefinitionIssue) {
	var issues []DefinitionIssue
	m, ok := raw.(map[string]any)
	if !ok {
		// a non-object schema is unsupported except as items inline
		return nil, append(issues, DefinitionIssue{Code: "invalidProfile", Path: spath, Message: "schema must be a JSON object"})
	}

	// $ref: must be the sole meaningful keyword, exact #/$defs/name form
	if ref, hasRef := m["$ref"].(string); hasRef {
		if len(m) != 1 {
			return nil, append(issues, DefinitionIssue{Code: "invalidReference", Path: spath, Message: "sibling keywords not allowed alongside $ref"})
		}
		if !strings.HasPrefix(ref, "#/$defs/") {
			return nil, append(issues, DefinitionIssue{Code: "invalidReference", Path: spath, Message: "must reference #/$defs/<name>"})
		}
		name := strings.TrimPrefix(ref, "#/$defs/")
		if visiting[name] {
			return nil, append(issues, DefinitionIssue{Code: "referenceCycle", Path: spath, Message: "cyclic $defs reference " + name})
		}
		def, ok := defs[name]
		if !ok {
			return nil, append(issues, DefinitionIssue{Code: "invalidReference", Path: spath, Message: "unknown $defs " + name})
		}
		visiting[name] = true
		n, iss := compileNode(def, defs, visiting, spath+"/"+name)
		visiting[name] = false
		return n, append(issues, iss...)
	}

	// reject unsupported keywords (object context)
	for k := range m {
		if !allowedKeys[k] {
			issues = append(issues, DefinitionIssue{Code: "unsupportedKeyword", Path: join(spath, escape(k)), Message: "unsupported keyword " + k})
		}
	}

	// root $defs handled by defsOf; only allowed at root
	if _, ok := m["$defs"]; ok && spath != "" {
		issues = append(issues, DefinitionIssue{Code: "unsupportedKeyword", Path: joink(spath, "$defs"), Message: "$defs only allowed at root"})
	}

	// anyOf/allOf/not rejected explicitly for clarity (they are not in allowedKeys anyway)

	if oneOf, ok := m["oneOf"].([]any); ok {
		n, iss := compileOneOf(oneOf, defs, visiting, spath)
		return n, append(issues, iss...)
	}

	typ, _ := m["type"].(string)
	n := &node{}
	switch typ {
	case "string":
		n.kind = kindString
	case "number":
		n.kind = kindNumber
	case "integer":
		n.kind = kindInteger
	case "boolean":
		n.kind = kindBoolean
	case "array":
		n.kind = kindArray
	case "object":
		n.kind = kindObject
	default:
		// a const/enum without an explicit type (e.g. a oneOf discriminator
		// like {"const":"manual"}) is valid; validate only equality/enum.
		if typ == "" && (m["const"] != nil || m["enum"] != nil) {
			n.kind = kindValue
		} else {
			return nil, append(issues, DefinitionIssue{Code: "unsupportedKeyword", Path: joink(spath, "type"), Message: "unsupported or missing type " + fmt.Sprint(m["type"])})
		}
	}

	// shared: enum / const
	n.enum = toAnySlice(m["enum"])
	if cv, ok := m["const"]; ok {
		n.hasConst = true
		n.constVal = cv
	}

	switch n.kind {
	case kindString:
		n.minLen = intFrom(m, "minLength", &issues, spath)
		n.maxLen = intFrom(m, "maxLength", &issues, spath)
	case kindNumber, kindInteger:
		n.min = floatFrom(m, "minimum", &issues, spath)
		n.max = floatFrom(m, "maximum", &issues, spath)
		n.exMin = floatFrom(m, "exclusiveMinimum", &issues, spath)
		n.exMax = floatFrom(m, "exclusiveMaximum", &issues, spath)
	case kindArray:
		if items, ok := m["items"]; ok {
			it, iss := compileNode(items, defs, visiting, joink(spath, "items"))
			n.items = it
			issues = append(issues, iss...)
		}
		n.minItems = intFrom(m, "minItems", &issues, spath)
		n.maxItems = intFrom(m, "maxItems", &issues, spath)
	case kindObject:
		if addl, ok := m["additionalProperties"]; ok {
			if b, ok := addl.(bool); !ok || b {
				issues = append(issues, DefinitionIssue{Code: "unsupportedKeyword", Path: joink(spath, "additionalProperties"), Message: "must be additionalProperties: false"})
			}
		} else {
			issues = append(issues, DefinitionIssue{Code: "unsupportedKeyword", Path: joink(spath, "additionalProperties"), Message: "object must declare additionalProperties: false"})
		}
		n.properties = map[string]*node{}
		if props, ok := m["properties"].(map[string]any); ok {
			for k, pv := range props {
				pn, iss := compileNode(pv, defs, visiting, join(joink(spath, "properties"), escape(k)))
				n.properties[k] = pn
				issues = append(issues, iss...)
			}
		}
		if req, ok := m["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					n.required = append(n.required, s)
				}
			}
		}
		if props, ok := m["properties"].(map[string]any); ok {
			for _, r := range n.required {
				if _, ok := props[r]; !ok {
					issues = append(issues, DefinitionIssue{Code: "fieldMismatch", Path: joink(joink(spath, "required"), escape(r)), Message: "required name not in properties"})
				}
			}
		}
	}
	return n, issues
}

func compileOneOf(branches []any, defs map[string]any, visiting map[string]bool, spath string) (*node, []DefinitionIssue) {
	n := &node{kind: kindOneOf}
	var issues []DefinitionIssue
	if len(branches) == 0 {
		return n, append(issues, DefinitionIssue{Code: "invalidDiscriminator", Path: spath, Message: "oneOf must have branches"})
	}
	var disc string
	for i, br := range branches {
		m, ok := br.(map[string]any)
		if !ok {
			return n, append(issues, DefinitionIssue{Code: "invalidDiscriminator", Path: fmt.Sprintf("%s/%d", spath, i), Message: "branch must be object"})
		}
		// every branch must be an object schema
		if t, _ := m["type"].(string); t != "object" {
			return n, append(issues, DefinitionIssue{Code: "invalidDiscriminator", Path: fmt.Sprintf("%s/%d", spath, i), Message: "branch must be object type"})
		}
		props, _ := m["properties"].(map[string]any)
		req, _ := m["required"].([]any)
		// find the discriminator: a required prop whose schema has a const
		var bDisc string
		for _, r := range req {
			rs, _ := r.(string)
			ps, ok := props[rs].(map[string]any)
			if !ok {
				continue
			}
			if _, hasC := ps["const"]; hasC {
				bDisc = rs
				break
			}
		}
		if bDisc == "" {
			return n, append(issues, DefinitionIssue{Code: "invalidDiscriminator", Path: fmt.Sprintf("%s/%d", spath, i), Message: "branch missing required const discriminator"})
		}
		if disc == "" {
			disc = bDisc
		} else if bDisc != disc {
			return n, append(issues, DefinitionIssue{Code: "invalidDiscriminator", Path: fmt.Sprintf("%s/%d", spath, i), Message: "branches must share the same discriminator"})
		}
		bn, iss := compileNode(br, defs, visiting, fmt.Sprintf("%s/%d", spath, i))
		n.branches = append(n.branches, bn)
		issues = append(issues, iss...)
	}
	n.discKey = disc
	// distinct discriminator consts
	seen := map[any]bool{}
	for _, b := range n.branches {
		p, ok := b.properties[disc]
		if !ok || !p.hasConst {
			return n, append(issues, DefinitionIssue{Code: "invalidDiscriminator", Path: spath, Message: "missing discriminator const"})
		}
		if seen[p.constVal] {
			return n, append(issues, DefinitionIssue{Code: "invalidDiscriminator", Path: spath, Message: "duplicate discriminator const"})
		}
		seen[p.constVal] = true
	}
	return n, issues
}

// Validate checks value against the compiled schema and returns normalized
// structural issues. It is strict: explicit null fails the type check for
// typed fields.
func (v *ValueSchema) Validate(value any) []StructuralIssue {
	issues := []StructuralIssue{}
	validateNode(v.root, value, "", &issues)
	return issues
}

func validateNode(n *node, value any, path string, issues *[]StructuralIssue) {
	if n == nil {
		return
	}

	// A concrete-type mismatch is reported once (code "type") and suppresses
	// sibling enum/const checks at this node, matching the normalized issue
	// contract. kindValue and kindOneOf have no standalone type to check here.
	if isConcreteKind(n.kind) && !typeMatches(n.kind, value) {
		*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "type", Path: path})
		return
	}

	// enum / const apply to any node kind once the type (if any) matched.
	if n.enum != nil && !contains(n.enum, value) {
		*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "enum", Path: path})
	}
	if n.hasConst && !reflect.DeepEqual(n.constVal, value) {
		*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "const", Path: path})
	}

	switch n.kind {
	case kindString:
		s := value.(string)
		runes := len([]rune(s))
		if n.minLen != nil && runes < *n.minLen {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "minLength", Path: path})
		}
		if n.maxLen != nil && runes > *n.maxLen {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "maxLength", Path: path})
		}
	case kindNumber, kindInteger:
		f, _ := asNumber(value)
		if n.min != nil && f < *n.min {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "minimum", Path: path})
		}
		if n.max != nil && f > *n.max {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "maximum", Path: path})
		}
		if n.exMin != nil && f <= *n.exMin {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "exclusiveMinimum", Path: path})
		}
		if n.exMax != nil && f >= *n.exMax {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "exclusiveMaximum", Path: path})
		}
	case kindArray:
		arr := value.([]any)
		if n.minItems != nil && len(arr) < *n.minItems {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "minItems", Path: path})
		}
		if n.maxItems != nil && len(arr) > *n.maxItems {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "maxItems", Path: path})
		}
		if n.items != nil {
			for i, el := range arr {
				validateNode(n.items, el, fmt.Sprintf("%s/%d", path, i), issues)
			}
		}
	case kindObject:
		obj := value.(map[string]any)
		for _, r := range n.required {
			if _, present := obj[r]; !present {
				*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "required", Path: join(path, escape(r))})
			}
		}
		for k := range obj {
			if _, known := n.properties[k]; !known {
				*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "additionalProperties", Path: join(path, escape(k))})
			}
		}
		for k, ps := range n.properties {
			if v, present := obj[k]; present {
				validateNode(ps, v, join(path, escape(k)), issues)
			}
		}
	case kindOneOf:
		obj, ok := value.(map[string]any)
		if !ok {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "type", Path: path})
			return
		}
		dv, present := obj[n.discKey]
		if !present {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "required", Path: join(path, escape(n.discKey))})
			return
		}
		matched := false
		for _, b := range n.branches {
			p := b.properties[n.discKey]
			if p.hasConst && reflect.DeepEqual(p.constVal, dv) {
				matched = true
				validateNode(b, value, path, issues)
				break
			}
		}
		if !matched {
			*issues = append(*issues, StructuralIssue{Source: "json-schema", Code: "discriminator", Path: join(path, escape(n.discKey))})
		}
	}
}

func isConcreteKind(k nodeKind) bool {
	switch k {
	case kindString, kindNumber, kindInteger, kindBoolean, kindArray, kindObject:
		return true
	default:
		return false
	}
}

func typeMatches(k nodeKind, value any) bool {
	switch k {
	case kindString:
		_, ok := value.(string)
		return ok
	case kindNumber:
		_, ok := asNumber(value)
		return ok
	case kindInteger:
		f, ok := asNumber(value)
		return ok && math.Trunc(f) == f
	case kindBoolean:
		_, ok := value.(bool)
		return ok
	case kindArray:
		_, ok := value.([]any)
		return ok
	case kindObject:
		_, ok := value.(map[string]any)
		return ok
	default:
		return true
	}
}

// helpers

func contains(list []any, v any) bool {
	for _, x := range list {
		if reflect.DeepEqual(x, v) {
			return true
		}
	}
	return false
}

func asNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	default:
		return 0, false
	}
}

func toAnySlice(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	default:
		return nil
	}
}

func intFrom(m map[string]any, key string, issues *[]DefinitionIssue, spath string) *int {
	if v, ok := m[key]; ok {
		f, isNum := asNumber(v)
		if !isNum {
			*issues = append(*issues, DefinitionIssue{Code: "invalidProfile", Path: joink(spath, key), Message: key + " must be a number"})
			return nil
		}
		return intp(f)
	}
	return nil
}

func floatFrom(m map[string]any, key string, issues *[]DefinitionIssue, spath string) *float64 {
	if v, ok := m[key]; ok {
		f, isNum := asNumber(v)
		if !isNum {
			*issues = append(*issues, DefinitionIssue{Code: "invalidProfile", Path: joink(spath, key), Message: key + " must be a number"})
			return nil
		}
		return &f
	}
	return nil
}

// escape escapes a JSON pointer reference token.
func escape(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}

func join(a, b string) string {
	if a == "" {
		return "/" + b
	}
	return a + "/" + b
}

func joink(a, b string) string { return join(a, escape(b)) }
