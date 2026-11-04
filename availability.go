// Registry-level availability: each manifest may declare one availability
// block — Umpire rules over declared conditions that decide which entries a
// consumer offers at load time. The block is a strict subset of the Umpire
// v1 JSON contract: rules may reference conditions only, never tool fields,
// because availability is evaluated without per-tool values.
package cliregistry

import (
	"encoding/json"
	"fmt"

	"github.com/umpire-tools/umpire-go"
)

// ToolAvailability is one tool's evaluated availability.
type ToolAvailability struct {
	// Available reports whether a consumer should offer the tool.
	Available bool
	// Reason is the first failure reason, or "" when available.
	Reason string
	// Reasons collects every failure reason in rule order.
	Reasons []string
}

// availability is the compiled form of a manifest's availability block.
type availability struct {
	// engine is the compiled Umpire schema (fields = tool names).
	engine *umpire.Umpire
	// conditions maps declared input name to its declared type.
	conditions map[string]string
}

// availabilityRuleTypes are the rule shapes availability accepts. Every form
// that references another tool's field (requires dependency/dependencies,
// oneOf, disables source, fairWhen, check) is rejected at load.
var availabilityRuleTypes = map[string]bool{
	"requires": true, "enabledWhen": true, "disables": true, "anyOf": true, "eitherOf": true,
}

// compileAvailability validates the raw availability block against the
// compiled tool set and compiles it to an engine. Rules may only reference
// declared conditions; every tool becomes a schema field.
func compileAvailability(raw json.RawMessage, toolNames []string) (*availability, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var block struct {
		Conditions map[string]json.RawMessage `json:"conditions"`
		Rules      []json.RawMessage          `json:"rules"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, fmt.Errorf("decode availability: %w", err)
	}

	// Narrow the rule vocabulary before hydration so failures name the
	// availability constraint, not the underlying Umpire semantics.
	conditions := map[string]string{}
	for name, defRaw := range block.Conditions {
		var def struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(defRaw, &def); err != nil {
			return nil, fmt.Errorf("decode availability condition %q: %w", name, err)
		}
		conditions[name] = def.Type
	}
	for i, ruleRaw := range block.Rules {
		if err := validateAvailabilityRule(ruleRaw, conditions); err != nil {
			return nil, fmt.Errorf("availability rule %d: %w", i, err)
		}
	}

	// Hydrate the Umpire schema: fields = every tool, rules and conditions
	// as declared. A generic decode gives Umpire properly typed JSON values
	// (float64 numbers, maps) while array order keeps rule order.
	fields := map[string]any{}
	for _, name := range toolNames {
		fields[name] = map[string]any{}
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("decode availability: %w", err)
	}
	generic["version"] = float64(1)
	generic["fields"] = fields
	schema, err := umpire.SchemaFromObject(generic)
	if err != nil {
		return nil, fmt.Errorf("availability: %w", err)
	}
	engine, err := umpire.NewFromSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("availability: %w", err)
	}
	return &availability{engine: engine, conditions: conditions}, nil
}

func validateAvailabilityRule(raw json.RawMessage, conditions map[string]string) error {
	var rule map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rule); err != nil {
		return fmt.Errorf("decode rule: %w", err)
	}
	var ruleType string
	if err := json.Unmarshal(rule["type"], &ruleType); err != nil {
		return fmt.Errorf("rule requires a string type")
	}
	if !availabilityRuleTypes[ruleType] {
		return fmt.Errorf(
			"rule type %q is not an availability rule; availability rules reference declared conditions only",
			ruleType,
		)
	}
	if ruleType == "requires" {
		if _, hasDependency := rule["dependency"]; hasDependency {
			return fmt.Errorf(
				`"requires" must use "when" with a condition expression; availability rules do not read tool fields`,
			)
		}
		if _, hasDependencies := rule["dependencies"]; hasDependencies {
			return fmt.Errorf(
				`"requires" must use "when" with a condition expression; availability rules do not read tool fields`,
			)
		}
	}
	if ruleType == "disables" {
		if _, hasSource := rule["source"]; hasSource {
			return fmt.Errorf(
				`"disables" must use "when" with a condition expression; availability rules do not read tool fields`,
			)
		}
	}
	if when, has := rule["when"]; has {
		return validateConditionExpr(when, conditions)
	}
	switch ruleType {
	case "anyOf":
		var inner struct {
			Rules []json.RawMessage `json:"rules"`
		}
		if err := json.Unmarshal(raw, &inner); err != nil {
			return fmt.Errorf("decode anyOf rules: %w", err)
		}
		for i, innerRule := range inner.Rules {
			if err := validateAvailabilityRule(innerRule, conditions); err != nil {
				return fmt.Errorf("anyOf rule %d: %w", i, err)
			}
		}
	case "eitherOf":
		var inner struct {
			Branches map[string][]json.RawMessage `json:"branches"`
		}
		if err := json.Unmarshal(raw, &inner); err != nil {
			return fmt.Errorf("decode eitherOf branches: %w", err)
		}
		for branchName, branchRules := range inner.Branches {
			for i, innerRule := range branchRules {
				if err := validateAvailabilityRule(innerRule, conditions); err != nil {
					return fmt.Errorf("eitherOf branch %q rule %d: %w", branchName, i, err)
				}
			}
		}
	}
	return nil
}

func validateConditionExpr(raw json.RawMessage, conditions map[string]string) error {
	var expr map[string]json.RawMessage
	if err := json.Unmarshal(raw, &expr); err != nil {
		return fmt.Errorf("decode expression: %w", err)
	}
	var op string
	if err := json.Unmarshal(expr["op"], &op); err != nil {
		return fmt.Errorf("expression requires a string op")
	}
	switch op {
	case "cond", "condEq", "condIn":
		var condition string
		if err := json.Unmarshal(expr["condition"], &condition); err != nil {
			return fmt.Errorf("%q expression requires a string condition", op)
		}
		if _, declared := conditions[condition]; !declared {
			return fmt.Errorf("expression references undeclared condition %q", condition)
		}
	case "and", "or":
		var inner struct {
			Exprs []json.RawMessage `json:"exprs"`
		}
		if err := json.Unmarshal(raw, &inner); err != nil {
			return fmt.Errorf("decode %q exprs: %w", op, err)
		}
		for i, innerExpr := range inner.Exprs {
			if err := validateConditionExpr(innerExpr, conditions); err != nil {
				return fmt.Errorf("%q expr %d: %w", op, i, err)
			}
		}
	case "not":
		var inner struct {
			Expr json.RawMessage `json:"expr"`
		}
		if err := json.Unmarshal(raw, &inner); err != nil {
			return fmt.Errorf("decode not expr: %w", err)
		}
		return validateConditionExpr(inner.Expr, conditions)
	default:
		return fmt.Errorf(
			"expression op %q is not a condition expression; availability rules reference declared conditions only",
			op,
		)
	}
	return nil
}

// AvailabilityInputs returns the declared availability condition inputs
// (name → declared type). Empty when the manifest declares no availability
// block. Consumers can use it to validate the inputs they supply.
func (r *Registry) AvailabilityInputs() map[string]string {
	if r.availability == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(r.availability.conditions))
	for name, typ := range r.availability.conditions {
		out[name] = typ
	}
	return out
}

// Availability evaluates the manifest's availability rules against the
// consumer's inputs. Every tool appears in the result; tools the block does
// not gate are always available. An error means the inputs failed the
// declared conditions (for example a missing input).
func (r *Registry) Availability(inputs map[string]any) (map[string]ToolAvailability, error) {
	out := map[string]ToolAvailability{}
	for _, name := range r.order {
		out[name] = ToolAvailability{Available: true, Reasons: []string{}}
	}
	if r.availability == nil {
		return out, nil
	}

	// Fields are tool names; availability rules read conditions only, so
	// no per-tool values exist to pass.
	values := map[string]any{}
	fieldStatus, err := r.availability.engine.Check(values, inputs, nil)
	if err != nil {
		return nil, err
	}
	for _, name := range r.order {
		status := fieldStatus[name]
		reasons := status.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		out[name] = ToolAvailability{
			Available: status.Enabled,
			Reason:    status.Reason,
			Reasons:   reasons,
		}
	}
	return out, nil
}
