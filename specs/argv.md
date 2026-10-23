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
- array: one `--flag <element>` pair per element; an empty array emits nothing
- object/other: **rejected at compile time**

### `short` (single-dash)
- scalar: append `-<short><value>` — the value is attached to the flag, this
  implementation has no separate-value short form
- boolean `true`: append `-<short>`; `false`: emit nothing
- array: append one `<short><element>` per element
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

## Dash-leading values on a separate-value binding

A value the engine emits as its own argv element after a flag must not begin
with `-`. `space` is such a style (`--flag <value>`): a value of `--recursive`,
`--`, `-1` or `-1.5` sits exactly where the command parses options, and the
command reads it as one the manifest never declared. Nothing in the argv
sequence can mark that element as data, so the value is refused at call
validation, before any argv is built:

- `Registry.Plan` and `Registry.Call` return a `*ValidationError`, identically
  and without spawning;
- the issue has `Source` `argv-binding`, `Code` `separateValueDash`, and an RFC
  6901 `Path` to the offending property (or array element, e.g. `/tags/1`);
- every string beginning with `-` is covered, `-` and `--` included, as is
  every negative number, and every element of an array bound that way.

Provenance is per binding: the check reads only the elements a binding emits,
never the command's fixed argv, so a fixed `--recursive` that happens to equal
an argument is not a refusal.

Use `equals` (`--flag=<value>`) to pass a value that begins with a dash — the
value stays inside one argv element, where no parser can read it as an option —
or the attached `short` form (`-l<value>`). Positional values keep the `--`
escape described above.

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
