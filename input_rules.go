package cliregistry

// Per-tool input rules: an optional manifest block of Umpire rules over the
// tool's own top-level properties. Rules compile once at load and are
// evaluated after the structural inputSchema, before anything is built.
//
// A rule that names a property gates the value the caller supplied for it, so
// an omitted property has nothing to violate. A requires or enabledWhen rule
// with no field constrains the whole invocation and runs on every call.
// Umpire's rules must name a field, so that gate is pinned to
// inputRuleInvocationField, a name a manifest property may not use.

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/umpire-tools/umpire-go"
)

// inputRuleInvocationField is the internal Umpire field an invocation-wide
// input rule compiles onto. NUL keeps it clear of any property a manifest
// would declare; one that declares it anyway fails load.
const inputRuleInvocationField = "\x00invocation"

// inputRuleTypes are the rule types an input-rule set accepts; fairness rules
// (fairWhen, check) and oneOf are refused at load.
var inputRuleTypes = map[string]bool{
	"requires": true, "enabledWhen": true, "disables": true, "anyOf": true, "eitherOf": true,
}

// inputRules is one tool's compiled input-rule engine.
type inputRules struct {
	engine *umpire.Umpire
}

// compileInputRules builds the Umpire engine from the declared rules, with one
// field per top-level property. The input wire is a strict subset of Umpire v1
// (see narrowInputRule). Umpire validates the rest of the contract: unknown
// properties, malformed expressions, and composite shapes.
func compileInputRules(raw json.RawMessage, props map[string]any) (*inputRules, error) {
	var rules []any
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("decode inputRules: %w", err)
	}
	if len(rules) == 0 {
		return nil, nil
	}
	if _, reserved := props[inputRuleInvocationField]; reserved {
		return nil, fmt.Errorf(
			"inputSchema property %q is reserved for invocation-wide input rules",
			inputRuleInvocationField,
		)
	}
	pinned := false
	for i, rule := range rules {
		rulePinned, err := narrowInputRule(rule)
		if err != nil {
			return nil, fmt.Errorf("input rule %d: %w", i, err)
		}
		pinned = rulePinned || pinned
	}

	fields := make(map[string]any, len(props)+1)
	for name := range props {
		fields[name] = map[string]any{}
	}
	if pinned {
		fields[inputRuleInvocationField] = map[string]any{}
	}
	schema, err := umpire.SchemaFromObject(map[string]any{
		"version": float64(1),
		"fields":  fields,
		"rules":   rules,
	})
	if err != nil {
		return nil, fmt.Errorf("compile inputRules: %w", err)
	}
	engine, err := umpire.NewFromSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("compile inputRules: %w", err)
	}
	return &inputRules{engine: engine}, nil
}

// narrowInputRule validates the input dialect and pins fieldless gates to the
// invocation field. It reports whether the rule or its operands needed a pin.
func narrowInputRule(raw any) (bool, error) {
	rule, ok := raw.(map[string]any)
	if !ok {
		return false, fmt.Errorf("rule must be an object, got %T", raw)
	}
	pinned := false
	ruleType, _ := rule["type"].(string)
	if !inputRuleTypes[ruleType] {
		return false, fmt.Errorf(
			"rule type %q is not an input rule; input rules gate a property or the invocation",
			ruleType,
		)
	}
	switch ruleType {
	case "requires", "enabledWhen":
		if ruleType == "requires" {
			if _, has := rule["dependency"]; has {
				return false, fmt.Errorf(`"requires" must use "when" with a property expression; input rules do not read dependencies`)
			}
			if _, has := rule["dependencies"]; has {
				return false, fmt.Errorf(`"requires" must use "when" with a property expression; input rules do not read dependencies`)
			}
		}
		// Only an absent subject and an empty string are fieldless: a null
		// member is already stripped by manifest normalization. Any other
		// value is left as declared so the Umpire schema rejects it; pinning
		// it here would silently widen a property gate to the invocation.
		field, isString := rule["field"].(string)
		if _, declared := rule["field"]; !declared || (isString && field == "") {
			rule["field"] = inputRuleInvocationField
			pinned = true
		}
	case "disables":
		if _, has := rule["source"]; has {
			return false, fmt.Errorf(`"disables" must use "when" with a property expression; input rules do not read a source property`)
		}
	}
	if inner, ok := rule["rules"].([]any); ok {
		for i, operand := range inner {
			operandPinned, err := narrowInputRule(operand)
			if err != nil {
				return false, fmt.Errorf("anyOf rule %d: %w", i, err)
			}
			pinned = operandPinned || pinned
		}
	}
	if branches, ok := rule["branches"].(map[string]any); ok {
		for _, name := range slices.Sorted(maps.Keys(branches)) {
			branch, _ := branches[name].([]any)
			for i, operand := range branch {
				operandPinned, err := narrowInputRule(operand)
				if err != nil {
					return false, fmt.Errorf("eitherOf branch %q rule %d: %w", name, i, err)
				}
				pinned = operandPinned || pinned
			}
		}
	}
	return pinned, nil
}

// inputRuleIssues evaluates the rules against the supplied args: the
// invocation-wide gate first, then supplied properties in name order. A
// property the caller omitted has no value to gate.
func (t *tool) inputRuleIssues(args map[string]any) ([]StructuralIssue, error) {
	if t.inputRules == nil {
		return nil, nil
	}
	status, err := t.inputRules.engine.Check(args, nil, nil)
	if err != nil {
		return nil, err
	}
	var issues []StructuralIssue
	if availability, ok := status[inputRuleInvocationField]; ok && !availability.Enabled {
		issues = append(issues, inputRuleIssue("", availability))
	}
	failed := make([]string, 0, len(args))
	for name := range args {
		if name == inputRuleInvocationField {
			continue
		}
		availability, ok := status[name]
		if !ok || availability.Enabled {
			continue
		}
		failed = append(failed, name)
	}
	sort.Strings(failed)
	for _, name := range failed {
		issues = append(issues, inputRuleIssue(propertyPointer(name), status[name]))
	}
	return issues, nil
}

// inputRuleIssue renders one failing subject: the rule's reason in Message,
// and Path naming the property (empty for an invocation-wide rule).
func inputRuleIssue(path string, availability umpire.FieldAvailability) StructuralIssue {
	reason := availability.Reason
	if reason == "" && len(availability.Reasons) > 0 {
		reason = strings.Join(availability.Reasons, "; ")
	}
	if reason == "" {
		reason = "input rules reject this invocation"
	}
	return StructuralIssue{
		Source:  "input-rules",
		Code:    "inputRule",
		Path:    path,
		Message: reason,
	}
}
