"""Turn Pydantic models into strict JSON Schemas that both providers accept.

Structured outputs on both APIs require every object to list all of its
properties as required and to forbid additional properties. Pydantic's
schema also uses $defs/$ref for nested models; we inline them so the schema
is self-contained.
"""

from __future__ import annotations

import copy
from typing import Any

from pydantic import BaseModel


def strict_schema(model: type[BaseModel]) -> dict[str, Any]:
    schema = model.model_json_schema()
    defs = schema.pop("$defs", {})

    def resolve(node: Any) -> Any:
        if isinstance(node, dict):
            if "$ref" in node:
                name = node["$ref"].split("/")[-1]
                return resolve(copy.deepcopy(defs[name]))
            out = {k: resolve(v) for k, v in node.items() if k not in ("title", "default")}
            if out.get("type") == "object" and "properties" in out:
                out["required"] = list(out["properties"])
                out["additionalProperties"] = False
            return out
        if isinstance(node, list):
            return [resolve(v) for v in node]
        return node

    resolved: dict[str, Any] = resolve(schema)
    return resolved
