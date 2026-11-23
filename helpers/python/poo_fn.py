"""poo_fn: register functions and run them as one-shot JSON tools.

stdin JSON args in, stdout JSON result out, nonzero exit is an error with
stderr as diagnostics. The helper does not validate arguments -- validation
is the registry's job before spawn; the helper only reads stdin, dispatches,
and writes the result.

Usage:

    import poo_fn

    @poo_fn.tool(
        {"type": "object", "properties": {"a": {"type": "number"}}},
        "Adds two numbers",
    )
    def add(a, b):
        return a + b

if __name__ == "__main__":
    poo_fn.main()
"""

import json
import sys

# Functions registered via @tool, keyed by name, with their declared
# schema and description kept alongside for inspection.
TOOLS = {}
METADATA = {}


def tool(schema, description):
    """Register the decorated function under its own name."""

    def decorator(fn):
        TOOLS[fn.__name__] = fn
        METADATA[fn.__name__] = {"schema": schema, "description": description}
        return fn

    return decorator


def main():
    """Read JSON args from stdin, dispatch on the op key, write the result."""
    raw = sys.stdin.read()
    try:
        args = json.loads(raw)
    except json.JSONDecodeError:
        sys.stderr.write("invalid JSON on stdin\n")
        sys.exit(1)
    if not isinstance(args, dict):
        sys.stderr.write("stdin must be a JSON object\n")
        sys.exit(1)
    op = args.pop("op", None)
    if op is None:
        sys.stderr.write("missing op key\n")
        sys.exit(1)
    if op not in TOOLS:
        sys.stderr.write("unknown op: %s\n" % op)
        sys.exit(1)
    try:
        result = TOOLS[op](**args)
    except Exception as exc:
        sys.stderr.write("%s: %s\n" % (type(exc).__name__, exc))
        sys.exit(1)
    sys.stdout.write(json.dumps(result))
    sys.exit(0)


if __name__ == "__main__":
    main()
