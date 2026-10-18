#!/usr/bin/env python3
"""Проверяет deploy/values/*.yaml по deploy/charts/service/values.schema.json
(имя файла == поле name; порты уникальны). Нужен: pip install pyyaml jsonschema."""
import glob, json, pathlib, sys
import yaml, jsonschema

ROOT = pathlib.Path(__file__).resolve().parents[2]
schema = json.loads((ROOT / "deploy/charts/service/values.schema.json").read_text())
ports, bad = {}, 0
for f in sorted(glob.glob(str(ROOT / "deploy/values/*.yaml"))):
    p = pathlib.Path(f)
    doc = yaml.safe_load(p.read_text(encoding="utf-8"))
    try:
        jsonschema.validate(doc, schema)
        if doc["name"] != p.stem:
            raise jsonschema.ValidationError("name %r != file name %r" % (doc["name"], p.stem))
        if doc["port"] in ports:
            raise jsonschema.ValidationError("port %s already used by %s" % (doc["port"], ports[doc["port"]]))
        ports[doc["port"]] = p.stem
        print("ok  ", p.name)
    except jsonschema.ValidationError as e:
        bad += 1
        print("FAIL", p.name, "-", e.message)
sys.exit(1 if bad else 0)
