"""An in-memory notes tool driven by the poo_fn helper.

Registered as the `notes` tool in examples/fn.json: the registry marshals
the `data` property as JSON to stdin (with the `op` key selecting the
function) and parses the JSON result on stdout. Each invocation of the
tool is a fresh process, so the store is per-call.
"""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "helpers" / "python"))
import poo_fn

# In-memory store: notes by id, next id to assign.
NOTES = {}
NEXT_ID = 1


@poo_fn.tool("Add a note and return it with its new id")
def add(title, body):
    global NEXT_ID
    n = {"id": NEXT_ID, "title": title, "body": body}
    NOTES[NEXT_ID] = n
    NEXT_ID += 1
    return n


@poo_fn.tool("Fetch a note by id; a missing id is an error")
def get(id):
    if id not in NOTES:
        raise KeyError("unknown id: %s" % id)
    return NOTES[id]


@poo_fn.tool("List all notes as {notes: [...]}")
def list():
    return {"notes": [NOTES[i] for i in sorted(NOTES)]}


if __name__ == "__main__":
    poo_fn.main()
