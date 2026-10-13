"""Refresh only the uploads public boundary; preserve concurrent owner contracts."""
import copy
import argparse
import json
import re
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
FILE = ROOT / "services/bff/internal/bff/openapi.json"
PUBLIC = {
    "catalog": {"get"}, "sessions": {"get", "post"},
    "sessions/{session}": {"get", "put"}, "sessions/{session}/reconcile": {"post"},
    "sessions/{session}/commit": {"post"}, "jobs": {"get"}, "jobs/{job}": {"get"},
    "jobs/{job}/report": {"get"}, "jobs/{job}/retry": {"post"},
    "jobs/{job}/recover": {"post"}, "jobs/{job}/revoke": {"post"},
    "files/register": {"post"}, "files/{file}": {"get"}, "files/{file}/download": {"get"},
    "receipts/{command}": {"get"}, "events": {"get"},
    "templates/{type}": {"get"},
}

def refs(value):
    if isinstance(value, dict):
        return {key: "#/components/schemas/Uploads_" + item.rsplit("/", 1)[-1]
                if key == "$ref" and isinstance(item, str) and item.startswith("#/components/schemas/")
                else refs(item) for key, item in value.items()}
    return [refs(item) for item in value] if isinstance(value, list) else value

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--owner-spec", type=Path, help="Saved public owner contract, without launching the owner")
    parser.add_argument("--draft", action="store_true", help="Label an unavailable/paused owner honestly")
    args = parser.parse_args()
    if args.owner_spec:
        owner = json.loads(args.owner_spec.read_text())
    else:
        with urllib.request.urlopen("http://127.0.0.1:8014/openapi.json", timeout=10) as response:
            owner = json.load(response)
    spec = json.loads(FILE.read_text())
    for path in list(spec["paths"]):
        if path.startswith("/bff/uploads/"): del spec["paths"][path]
    schemas = spec.setdefault("components", {}).setdefault("schemas", {})
    for name in list(schemas):
        if name.startswith("Uploads_"): del schemas[name]
    for name, schema in owner.get("components", {}).get("schemas", {}).items():
        schemas["Uploads_" + name] = refs(schema)
    for tail, methods in PUBLIC.items():
        path = "/bff/uploads/" + tail
        spec["paths"][path] = {}
        for method in methods:
            operation = refs(copy.deepcopy(owner["paths"]["/v1/uploads/divisions/{division}/" + tail][method]))
            operation["security"] = [{"sessionCookie": []}]
            operation["parameters"] = [p for p in operation.get("parameters", []) if p["name"] != "division"]
            operation["parameters"] += [
                {"name": "X-CPM-Request", "in": "header", "required": True, "schema": {"const": "1"}},
                {"name": "X-CPM-Context", "in": "header", "required": True, "schema": {"type": "string"}},
            ]
            operation["description"] = "Organization/division/actor are fixed by the live BFF session. Writes require the configured Origin. Stale context returns409. Owner permission checks remain authoritative."
            if args.draft:
                operation["description"] += " Draft integration: uploads owner is paused while its execution queue is reconsidered. No live import workflow is currently verified."
            if tail.endswith("/download"):
                operation["responses"]["200"]["content"] = {"application/octet-stream": {"schema": {"type": "string", "format": "binary"}}}
            expected = set(re.findall(r"\{([^}]+)\}", path))
            declared = {p["name"] for p in operation["parameters"] if p.get("in") == "path" and p.get("required")}
            if expected != declared: raise ValueError(f"Invalid path parameters: {path}")
            spec["paths"][path][method] = operation
    if "uploads" not in spec["info"]["title"]: spec["info"]["title"] += ", uploads"
    FILE.write_text(json.dumps(spec, ensure_ascii=False, indent=2) + "\n")
    print("Embedded uploads public operations:", sum(map(len, PUBLIC.values())))

if __name__ == "__main__": main()
