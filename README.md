# cli-registry

Turn pre-registered CLI commands into typed tool calls.

A **registered CLI** is a small manifest describing a real command-line
interface—argv, flags, and an input JSON Schema. `cli-registry` compiles it into
a standard tool, validates arguments against the schema before spawning,
executes the command through a bounded process engine, and decodes the result.
A tool is a data file, not a rebuild.

## Why

Adding features to a harness normally requires one of two approaches:

- **Skill file** — prose the model reads and improvises from (fragile).
- **MCP server** — a live Model Context Protocol (MCP) JSON-RPC process for what is often a shell command.

`cli-registry` is the third thing: **tools-as-data**. A manifest is compiled to a
standard MCP tool descriptor; anything that already runs processes can consume
it. MCP without the server; a skill with typed parameters.

## Quickstart

```sh
# validate a registry (load + compile, no spawn)
cli-registry validate examples/example.json

# call a tool
cli-registry run -registry examples/example.json say '{"message":"hello"}'
```

`examples/writetighter.json` registers a real tool — `writetighter`'s deterministic `lint` — so you can watch a genuine CLI (positional arg, `--kind` flag, enum) flow through the pipeline.

Subscribe as an MCP server:

```sh
# on-demand stdio
cli-registry-mcp -registry examples

# shared Streamable HTTP daemon (mid-session updates supported)
CLI_REGISTRY_TOKEN=$(openssl rand -hex 16) \
  cli-registry-mcp -transport http -addr 127.0.0.1:9100 -registry examples
```

Point Claude/opencode at `url: http://127.0.0.1:9100` (bearer token) or at a
`command:` that runs `cli-registry-mcp -registry ...`.

## Manifest

JSON, YAML, or TOML (by extension), normalized to a JSON intermediate at ingest.
See [`specs/manifest.md`](specs/manifest.md) and [`specs/argv.md`](specs/argv.md)
for the normative contract.

```jsonc
{
  "version": 1,
  "tools": {
    "gh-pr-list": {
      "description": "List pull requests with optional state/limit filters",
      "command": ["gh", "pr", "list"],
      "env": { "GH_TOKEN": "env:GH_TOKEN" },   // explicit allow-list
      "timeout": "30s",
      "inputSchema": {                          // one root schema per tool
        "type": "object",
        "properties": {
          "state": { "type": "string", "enum": ["open", "closed", "merged"] },
          "limit": { "type": "integer", "minimum": 1 }
        },
        "additionalProperties": false
      },
      "bindings": [                              // args -> argv
        { "property": "state", "style": "equals", "flag": "--state" },
        { "property": "limit", "style": "space", "flag": "--limit" }
      ],
      "output": { "mode": "raw" }                 // raw | json | json-pointer
    }
  }
}
```

## Layout

| Path | Purpose |
|---|---|
| `manifest/` | format detection, normalization, discovery, null-strip |
| `schema/` | closed-vocabulary JSON Schema compiler + validator (Option B) |
| root `cliregistry` package | Registry / Tool / Call + process engine |
| `cmd/cli-registry` | `validate` + `run` CLI |
| `cmd/cli-registry-mcp` | stdio + Streamable HTTP MCP server |
| `conformance/` | vendored umpire-spec profile fixtures (pinned) |
| `specs/` | normative manifest + argv contracts |

## Conformance

The schema validator matches the umpire-json-schema profile v1 structural subset
(`conformance/`, pinned to `umpire-spec v1.1.0-rc.2`). `make fixtures-verify`
checks the vendored fixtures' integrity; `make verify` runs the whole gate.

## Security model

- **Explicit registry** — only pre-registered surfaces are exposed; never
  auto-mirror a CLI.
- **No shell** — argv passes to `exec.Command` directly (no injection).
- **Env allow-list** — only declared variables (plus a benign default set);
  `env:NAME` passthrough must be declared.
- **cwd containment** — optional `allow` roots, canonicalized.
- **Timeout + process-group kill** — descendants are reaped.
- **HTTP daemon** — loopback bind by default, bearer auth, Host/Origin checks,
  1 MiB request limit.

## License

Apache-2.0. Vendored conformance fixtures retain their own upstream licensing;
see [`conformance/README.md`](conformance/README.md).
