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
// (see validateInputRuleShape); Umpire validates the rest of the contract,
// including unknown properties, malformed expressions, and composites.
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
	for i, rule := range rules {
		if err := validateInputRuleShape(rule); err != nil {
			return nil, fmt.Errorf("input rule %d: %w", i, err)
		}
	}
	pinned := pinInvocationRules(rules)

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

// validateInputRuleShape narrows the rule vocabulary before hydration.
func validateInputRuleShape(raw any) error {
	rule, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("rule must be an object, got %T", raw)
	}
	ruleType, _ := rule["type"].(string)
	if !inputRuleTypes[ruleType] {
		return fmt.Errorf(
			"rule type %q is not an input rule; input rules gate a property or the invocation",
			ruleType,
		)
	}
	switch ruleType {
	case "requires":
		if _, has := rule["dependency"]; has {
			return fmt.Errorf(`"requires" must use "when" with a property expression; input rules do not read dependencies`)
		}
		if _, has := rule["dependencies"]; has {
			return fmt.Errorf(`"requires" must use "when" with a property expression; input rules do not read dependencies`)
		}
	case "disables":
		if _, has := rule["source"]; has {
			return fmt.Errorf(`"disables" must use "when" with a property expression; input rules do not read a source property`)
		}
	}
	if inner, ok := rule["rules"].([]any); ok {
		for i, operand := range inner {
			if err := validateInputRuleShape(operand); err != nil {
				return fmt.Errorf("anyOf rule %d: %w", i, err)
			}
		}
	}
	if branches, ok := rule["branches"].(map[string]any); ok {
		for _, name := range slices.Sorted(maps.Keys(branches)) {
			branch, _ := branches[name].([]any)
			for i, operand := range branch {
				if err := validateInputRuleShape(operand); err != nil {
					return fmt.Errorf("eitherOf branch %q rule %d: %w", name, i, err)
				}
			}
		}
	}
	return nil
}

// pinInvocationRules gives every fieldless gate rule the internal invocation
// field, recursing through composite operands, and reports whether any rule
// needed it.
func pinInvocationRules(rules []any) bool {
	pinned := false
	for _, rule := range rules {
		pinned = pinInvocationRule(rule) || pinned
	}
	return pinned
}

func pinInvocationRule(raw any) bool {
	rule, _ := raw.(map[string]any)
	if rule == nil {
		return false
	}
	pinned := false
	switch rule["type"] {
	case "requires", "enabledWhen":
		if field, _ := rule["field"].(string); field == "" {
			rule["field"] = inputRuleInvocationField
			pinned = true
		}
	}
	if inner, ok := rule["rules"].([]any); ok {
		pinned = pinInvocationRules(inner) || pinned
	}
	if branches, ok := rule["branches"].(map[string]any); ok {
		for _, name := range slices.Sorted(maps.Keys(branches)) {
			branch, _ := branches[name].([]any)
			pinned = pinInvocationRules(branch) || pinned
		}
	}
	return pinned
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
