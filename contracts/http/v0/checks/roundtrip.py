"""PYTHONPATH must contain the generated quivr_client package."""
import importlib
import json
from pathlib import Path
import re

cases = json.loads((Path(__file__).resolve().parents[1] / "examples.json").read_text())
for case in cases:
    name = case["schema"]
    module = re.sub(r"(?<!^)(?=[A-Z])", "_", name).lower()
    model = getattr(importlib.import_module("quivr_client.models." + module), name)
    output = json.loads(model.from_dict(case["value"]).to_json())
    assert output == case["value"], (case["name"], output)
print(f"Python: {len(cases)} JSON round trips passed")
