# cli-registry manifest spec (frozen, Stage 0)

Normative contract for cli-registry manifests. This document is the single
source of truth; implementations must match it exactly.

## Format & normalization

- Manifests may be **JSON**, **YAML** (`.yaml`/`.yml`), or **TOML** (`.toml`), detected by file extension; unknown extensions default to JSON.
- All non-JSON formats normalize to a JSON intermediate at ingest, then are decoded with `DisallowUnknownFields` (unknown fields rejected) and single-value enforcement (trailing data / multiple documents rejected).
- Every manifest carries a `version` (integer). Unknown major version → load error.
- **Null normalization:** before validation and argv construction, an explicit pass recursively strips `null` object members. `null` array elements remain and are invalid. A required property whose value was `null` reports as missing.
  - Rationale: for CLI registration, "null" and "absent" are equivalent. This pass makes them identical everywhere downstream.
- Schema values are authored as **native mappings/structures** (never JSON-string blobs).

## Registry & tool shape

Top level: `version`, `tools` (array or map — see load rules).

Each tool:

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | auto | derived from tool key / map key |
| `description` | string | yes | one-line intent for the model |
| `command` | array<string> | yes | argv prefix; appended by the binding table |
| `env` | map | no | allow-list; value is literal or `"env:NAME"` (context → child) |
| `cwd` | object | no | `{ mode: "allow"|"inherit", roots: []string }`; relative roots resolve against the manifest file |
| `timeout` | string (Go duration) | yes | mandatory |
| `inputSchema` | object | yes | **one** JSON Schema per tool (below) |
| `bindings` | object/array | yes | input binding table keyed by top-level property (see argv.md) |
| `output` | object | yes | `{ mode: "raw"|"json"|"json-pointer", json_pointer?: string, success_exit_codes?: []int }` (default `[0]`) |

## inputSchema model

Exactly **one** root `inputSchema` per tool. It may contain local `$defs` and
`#/$defs/...` references; `additionalProperties: false` at every object level.

Supported vocabulary is exactly the umpire-json-schema profile v1 subset:

- `type`: `string`, `number`, `integer`, `boolean`, `array`, `object`
- `properties`, `required`, `additionalProperties: false`
- `items` (single homogeneous schema), `minItems`, `maxItems`
- `minLength`, `maxLength`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`
- `enum`, `const`
- local acyclic `$defs` + exact `{ "$ref": "#/$defs/<name>" }`
- tagged `oneOf` (object branches with a shared required discriminator + distinct `const`)

Anything else (including `anyOf`, `allOf`, `not`, `pattern`, `format`, `uniqueItems`,
remote/recursive refs) **fails compilation** — never silently ignored.

## Discovery

Each binary receives registry path(s) via flag or `CLI_REGISTRY` env. A path is a
file, a directory (all manifests within, deterministic sort), or repeated paths.
Duplicate tool names → load error. Load is **all-or-nothing**: any failure rejects
the whole registry.

## Availability

Optional top-level `availability` object. At most one across the merged
registry; a repeat is a load error.

The block decides, at load time, which entries a consumer offers. It is a
strict subset of the [Umpire](https://github.com/umpire-tools/umpire-spec) v1
JSON contract, evaluated by the runtime with inputs the consumer supplies:

- `conditions` (object, optional): the inputs the consumer supplies at load,
  each `{ "type": "boolean"|"string"|"number"|"string[]"|"number[]",
  "description"?: string }`.
- `rules` (array, optional): Umpire rules that gate each tool's availability.
  The tool set is implicit — every tool is a Umpire field; an ungated tool is
  always available.

Allowed rule forms, evaluated in declaration order (first failure supplies
the tool's `reason`):

- `enabledWhen`, `requires`, `disables` — each with a condition expression in
  `when`; `requires` and `disables` must use the `when` form, not the
  field-dependency form.
- `anyOf` (`rules`) and `eitherOf` (`branches`) over the above.

Allowed expression ops: `cond`, `condEq`, `condIn`, `and`, `or`, `not` —
condition references only. Field-bound ops (`eq`, `in`, `check`, ...) are
rejected at load: availability is evaluated without per-tool values, so a
field-bound rule could never pass.

Schema-level validation (unknown members, unknown conditions, bad types,
empty composites) reuses the Umpire contract and fails the whole load.

Consumer surface:

- `Registry.AvailabilityInputs() map[string]string` — declared condition
  inputs (name → type), for validating supplied inputs.
- `Registry.Availability(inputs map[string]any) (map[string]ToolAvailability,
  error)` — evaluated availability per tool (`Available`, `Reason`,
  `Reasons`). A missing input fails with the Umpire error.

Deciding what to do with an unavailable entry (drop it, or list it with its
reason) is the consumer's call; the runtime returns both paths.

## Env semantics

- Default: the child inherits **only** declared `env` variables (no blanket
  pass-through of arbitrary env). Context env explicitly declared as `"env:NAME"`
  is copied through; a missing context `NAME` is a load/definition error.
- Never global `os.Setenv` mutation.

## cwd semantics

- `allow`: each relative `root` canonicalizes against the manifest file's directory;
  the resolved cwd must canonically fall within an allowed root, else execution fails.
- `inherit`: child inherits the parent process cwd (caller-plumbed, not global `os.Chdir`).
