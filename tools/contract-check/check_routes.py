#!/usr/bin/env python3
"""Сверка трёх описаний периметра (S5):

  1. services/gateway/internal/routes/routes.json  — таблица, по которой gateway реально маршрутизирует;
  2. contracts/openapi/gateway.yaml  x-routes      — то же для людей/ревью;
  3. contracts/openapi/<сервис>.yaml paths         — операции сервисов.

Ошибка, если: (1)≠(2); маршрут указывает на операцию, которой нет в контракте сервиса; операция сервиса
(не x-internal, не webhook) не проведена через gateway и не перечислена в NOT_ROUTED с причиной.
"""
import json
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
OPENAPI = ROOT / "contracts/openapi"
UPSTREAM_SPEC = {"auth": "auth", "restaurant": "restaurant", "menu": "menu", "orders": "orders", "payment": "payment", "delivery": "delivery", "loyalty": "loyalty", "notification": "notification"}
METHODS = {"get", "post", "put", "patch", "delete"}

# Операции контрактов, которые намеренно НЕ публикуются через gateway (ключ: "METHOD /path").
NOT_ROUTED = {
    "auth": {},
    "restaurant": {},
    "menu": {},
    "orders": {},
    "payment": {},
    "delivery": {},
    "loyalty": {},
    "notification": {},
}


def operations(spec_name):
    spec = yaml.safe_load((OPENAPI / f"{spec_name}.yaml").read_text())
    out = {}
    for path, item in (spec.get("paths") or {}).items():
        for m, op in item.items():
            if m in METHODS and isinstance(op, dict):
                out[f"{m.upper()} {path}"] = op
    return out


def main():
    errs = []
    table = json.loads((ROOT / "services/gateway/internal/routes/routes.json").read_text())
    gw = yaml.safe_load((OPENAPI / "gateway.yaml").read_text())
    doc = gw.get("x-routes") or []

    def norm(r):
        return {
            "upstream": r["upstream"], "access": r["access"], "roles": sorted(r.get("roles") or []),
            "sensitive": bool(r.get("sensitive")), "limit": r.get("limit") or "",
        }

    a = {r["pattern"]: norm(r) for r in table}
    b = {r["pattern"]: norm(r) for r in doc}
    for p in sorted(set(a) - set(b)):
        errs.append(f"routes.json has {p!r}, gateway.yaml x-routes does not")
    for p in sorted(set(b) - set(a)):
        errs.append(f"gateway.yaml x-routes has {p!r}, routes.json does not")
    for p in sorted(set(a) & set(b)):
        if a[p] != b[p]:
            errs.append(f"{p}: routes.json {a[p]} != x-routes {b[p]}")

    ops = {name: operations(spec) for name, spec in UPSTREAM_SPEC.items()}
    routed = {name: set() for name in ops}
    for r in table:
        up = r["upstream"]
        if r["pattern"] not in ops.get(up, {}):
            errs.append(f"{r['pattern']}: no such operation in {UPSTREAM_SPEC.get(up, up)}.yaml")
        else:
            routed[up].add(r["pattern"])

    for up, o in ops.items():
        for key, op in sorted(o.items()):
            if key in routed[up] or key in NOT_ROUTED[up]:
                continue
            if op.get("x-internal") or "/webhooks/" in key or key.split(" ", 1)[1].startswith(("/actuator", "/health", "/metrics")):
                continue
            errs.append(f"{up}.yaml: {key} is public in the contract but neither routed by gateway nor listed in NOT_ROUTED")

    if errs:
        print("check-routes: FAILED")
        for e in errs:
            print("  -", e)
        return 1
    n = sum(len(v) for v in routed.values())
    print(f"check-routes: ok ({len(table)} routes, {n} upstream operations matched)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
