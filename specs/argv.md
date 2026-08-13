# cli-registry argv-binding spec (frozen, Stage 0)

Normative contract for converting a validated tool-call value into an argv
sequence. This document is the single source of truth for the execution
engine's argv construction.

## Binding table

`bindings` is an ordered list of rows that reference top-level `inputSchema`
properties. Each row:

| Field | Type | Meaning |
|---|---|---|
| `property` | string | must match a top-level `inputSchema` property (exact correspondence; see below) |
| `style` | enum | `equals` \| `space` \| `short` \| `positional` |
| `flag` | string | flag text (required for `equals`/`space`/`short`); e.g. `--state` |
| `short` | string | short flag (only for `short` style) |

Exact-correspondence rule: the set of `bindings[].property` must equal the set of
top-level `inputSchema` properties, and `additionalProperties:false`. This is
checked at registry compile time; a mismatch fails load.

## Conversion rules (normative)

Let `v` be the validated value for a bound property and `s` its schema type.

### `equals` (flag + `=`)
- scalar: append `--flag=<value>` (see scalar encoding below)
- boolean `true`: append `--flag`; boolean `false`: emit nothing
- array: append one `--flag=<element>` per element
- object/other: **rejected at compile time**

### `space` (flag then separate value)
- scalar: append `--flag`, then `<value>`
- boolean `true`: append `--flag`; `false`: emit nothing
- array: append `--flag`, then each element on its own; `--flag` shown only once
- object/other: **rejected at compile time**

### `short` (single-dash)
- scalar: append `-<short>` (boolean true) or `-<short><value>` / `-<short> <value>` per equals-vs-space sub-style
- array: append one short flag per element
- object/other: rejected at compile time

### `positional`
- scalar (string/number/boolean): append `<value>` directly
- array: append each element directly
- object: **rejected at compile time**

## Scalar encoding

| Schema type | argv string |
|---|---|
| `string` | the value as-is |
| `integer` | base-10 digits |
| `number` | shortest reliable decimal representation |
| `boolean` | only meaningful via flag styles (`true` → flag present; `false` → omitted); **not** emitted as `"true"`/`"false"` in positional form — rejected at compile time for positional |

## `--` handling

When a binding row or the command's positional tail may begin with a value that
starts with `-`, the engine inserts `--` before that positional value to prevent
it being parsed as a flag. The engine never injects literal shell syntax; all argv
elements are passed to `exec.Command` directly (no shell, no shell injection).

## Compile-time rejections (fail registry load, never silent)

- `property` not in `inputSchema`, or top-level property with no binding (correspondence mismatch).
- `object`/`other` type on any binding style (objects cannot be flattened to argv in v1).
- `boolean` in `positional` style.
- Unsupported `style` value.
- Schema×style combos not enumerated above.

## Examples

```jsonc
// --state=merged
{ "style": "equals", "property": "state", "flag": "--state" }

// --limit 30
{ "style": "space", "property": "limit", "flag": "--limit" }

// -a / -b / -c  (repeated flags for an array)
{ "style": "short", "property": "labels", "flag": "-l" }

// positional tail
{ "style": "positional", "property": "paths" }
```
