# Migration notes

How existing "skill file" and "hand-rolled MCP server" patterns map onto
`cli-registry`.

## From a skill file (prose-as-spec)

A Claude/Codex skill is prose the model reads and improvises from. To convert a
skill whose body is really "run this command with these args":

1. Extract the command into a manifest tool (`command`, `inputSchema`,
   `bindings`, `output`).
2. Move the prose into the tool's one-line `description` (all that remains
   prose by design — args become typed data).
3. Point the harness at the registry (`CLI_REGISTRY=<dir>`) instead of mounting
   a skill file.

This migration removes the prose that spells out how to construct flags and eliminates errors from manual shell syntax. The model now only selects argument values; `cli-registry` owns validation and argv construction.

## From a hand-rolled MCP server

A one-tool MCP server is an entire JSON-RPC process for a shell command. To
collapse it:

1. Write the tool as a manifest entry.
2. Run the bundled `cli-registry-mcp` (shared daemon if you want mid-session
   updates via `notifications/tools/list_changed`, or stdio for on-demand).
3. Delete the handwritten `tools/list` / `tools/call` / transport code.

Keep a custom MCP server only when the tool needs state across calls or
non-tool surfaces (resources, prompts) that a one-shot CLI doesn't provide.

## Version / compatibility

- Manifests carry `version: 1`; unknown major versions are rejected.
- `schema/` implements the closed umpire-profile subset; unsupported JSON
  Schema keywords fail compilation rather than degrading silently.
