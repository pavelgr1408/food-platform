#!/usr/bin/env python3
"""Проверки периметра через настоящий ingress (долг 37). Только stdlib; kubectl нужен лишь для секции A.

  A. Топология (kubectl): ровно два Ingress (gateway «/», payment «/webhooks/payments»), все Service — ClusterIP,
     pod'ы живы без перезапусков, TRUSTED_PROXY_CIDRS gateway покрывает IP pod'а ingress-controller.
  B. HTTP через ingress: /metrics наружу не виден; внутренние пути auth закрыты; вебхуки идут МИМО gateway
     (подпись проверяет сам payment); лимит тела на ingress.
  C. Лимиты (запускаются последними, «сжигают» бакеты): ingress по IP на вебхуках; gateway по реальному IP клиента,
     устойчивость к подделке X-Forwarded-For; другой реальный клиент лимитом первого не затронут.

Код возврата 0 — все проверки прошли. Отладка без кластера (эмуляция ingress, см. docs/dev.md):
  --no-kubectl --source 127.0.0.2 --alt-source 127.0.0.3
"""
import argparse
import concurrent.futures as cf
import hashlib
import hmac
import http.client
import json
import subprocess
import sys
import time
import urllib.parse

GW_SECRET = b"dev-gateway-webhook-secret"
POS_SECRET = b"dev-pos-webhook-secret"

ap = argparse.ArgumentParser()
ap.add_argument("--ingress-url", default="http://localhost")
ap.add_argument("--host", default="", help="заголовок Host (по умолчанию — хост из --ingress-url)")
ap.add_argument("--namespace", default="food")
ap.add_argument("--no-kubectl", action="store_true", help="пропустить секцию A и pod-клиента")
ap.add_argument("--source", default="", help="локальный адрес исходящих соединений (отладка)")
ap.add_argument("--alt-source", default="", help="второй «реальный клиент» по другому source-IP (отладка); иначе pod через kubectl")
ap.add_argument("--auth-requests", type=int, default=60, help="сколько неверных логинов слать для проверки лимита auth")
ap.add_argument("--skip-limits", action="store_true", help="не выполнять секцию C")
ap.add_argument("--ingress-svc", default="ingress-nginx-controller.ingress-nginx.svc", help="адрес controller внутри кластера (для pod-клиента)")
args = ap.parse_args()

u = urllib.parse.urlparse(args.ingress_url)
HOST, PORT = u.hostname, u.port or 80
HOSTHDR = args.host or (u.hostname if not u.port else f"{u.hostname}:{u.port}")

results = []


def check(name, ok, detail=""):
    results.append(ok)
    print(("  ok    " if ok else "  FAIL  ") + name + (f"   [{detail}]" if detail and not ok else ""))
    return ok


def req(method, path, headers=None, body=None, source=None, timeout=15):
    h = {"Host": HOSTHDR, "Connection": "close"}
    h.update(headers or {})
    src = (source if source is not None else args.source) or None
    try:
        c = http.client.HTTPConnection(HOST, PORT, timeout=timeout, source_address=(src, 0) if src else None)
        c.request(method, path, body=body, headers=h)
        r = c.getresponse()
        data = r.read()
        out = (r.status, dict(r.getheaders()), data)
        c.close()
        return out
    except Exception as e:  # сетевой сбой = код 0, проверка упадёт с понятным detail
        return (0, {}, str(e).encode())


def sign(secret, body, ts=None):
    ts = int(time.time()) if ts is None else ts
    mac = hmac.new(secret, f"{ts}.".encode() + body, hashlib.sha256).hexdigest()
    return f"t={ts},v1={mac}"


def jcode(data):
    try:
        return json.loads(data).get("code", "")
    except Exception:
        return ""


def kubectl(*a):
    return subprocess.run(["kubectl", "-n", args.namespace, *a], capture_output=True, text=True, timeout=120)


# ---------------------------------------------------------------- A. топология
def section_a():
    print("== A. топология кластера (kubectl)")
    r = kubectl("get", "ingress", "-o", "json")
    if r.returncode != 0:
        return check("kubectl get ingress", False, r.stderr.strip())
    items = json.loads(r.stdout)["items"]
    routes = {}
    for ing in items:
        for rule in ing["spec"].get("rules", []):
            for p in rule["http"]["paths"]:
                routes[p["path"]] = p["backend"]["service"]["name"]
    check("ровно два Ingress: «/» → gateway, «/webhooks/payments» → payment",
          routes == {"/": "gateway", "/webhooks/payments": "payment"}, str(routes))
    pay = next((i for i in items if i["metadata"]["name"] == "payment"), None)
    ann = (pay or {}).get("metadata", {}).get("annotations", {})
    check("ingress payment: limit-rps и proxy-body-size на месте",
          ann.get("nginx.ingress.kubernetes.io/limit-rps") and ann.get("nginx.ingress.kubernetes.io/proxy-body-size"), str(ann))

    svcs = json.loads(kubectl("get", "svc", "-o", "json").stdout)["items"]
    bad = [s["metadata"]["name"] for s in svcs if s["spec"].get("type", "ClusterIP") != "ClusterIP"]
    check("все Service — ClusterIP (наружу только через ingress)", not bad, str(bad))
    gw = next((s for s in svcs if s["metadata"]["name"] == "gateway"), None)
    ports = {p["name"]: p["port"] for p in (gw or {}).get("spec", {}).get("ports", [])}
    check("Service gateway публикует порт metrics отдельно от http (9100)", ports.get("metrics") == 9100 and "http" in ports, str(ports))
    ing_ports = {p["backend"]["service"]["port"].get("name") for ing in items for rule in ing["spec"]["rules"] for p in rule["http"]["paths"]}
    check("ingress смотрит только на порт http", ing_ports == {"http"}, str(ing_ports))

    pods = json.loads(kubectl("get", "pods", "-o", "json").stdout)["items"]
    broken = []
    for p in pods:
        ph = p["status"].get("phase")
        rc = sum(c.get("restartCount", 0) for c in p["status"].get("containerStatuses", []))
        if ph not in ("Running", "Succeeded") or rc:
            broken.append(f'{p["metadata"]["name"]}: {ph}, restarts={rc}')
    check("pod'ы Running/Succeeded без перезапусков", not broken, "; ".join(broken))

    cm = json.loads(kubectl("get", "configmap", "gateway-config", "-o", "json").stdout)["data"]
    cidrs = cm.get("TRUSTED_PROXY_CIDRS", "")
    r = subprocess.run(["kubectl", "-n", "ingress-nginx", "get", "pods", "-l", "app.kubernetes.io/component=controller",
                        "-o", "jsonpath={.items[*].status.podIP}"], capture_output=True, text=True, timeout=60)
    ips = r.stdout.split()
    import ipaddress
    nets = [ipaddress.ip_network(c.strip(), strict=False) for c in cidrs.split(",") if c.strip()]
    check(f"TRUSTED_PROXY_CIDRS gateway ({cidrs}) покрывает pod ingress-controller {ips}",
          bool(ips) and all(any(ipaddress.ip_address(i) in n for n in nets) for i in ips))
    print(f"  info  controller pod IP: {' '.join(ips)}  → TRUSTED_PROXY_CIDRS для values: подсеть pod'ов вашего кластера, а не /32")


# ---------------------------------------------------------------- B. HTTP через ingress
def section_b():
    print("== B. HTTP через ingress")
    s, _, _ = req("GET", "/actuator/health")
    check("GET /actuator/health → 200 (через gateway)", s == 200, s)
    s, _, d = req("GET", "/auth/.well-known/jwks.json")
    check("JWKS публичен через gateway", s == 200 and b'"keys"' in d, s)
    for path in ("/metrics", "//metrics", "/webhooks/payments/metrics", "/auth/metrics", "/%6detrics"):
        s, _, d = req("GET", path)
        check(f"GET {path} наружу не отдаётся", s != 200 and b"# HELP" not in d and b"go_goroutines" not in d, f"{s} {d[:80]!r}")
    for path in ("/auth/introspect", "/auth/authorize"):
        s, _, d = req("POST", path, {"Content-Type": "application/json"}, b"{}")
        check(f"POST {path} через gateway → 404", s == 404, f"{s} {d[:80]!r}")
    s, _, d = req("GET", "/restaurants")
    check("GET /restaurants → 200 (маршрут gateway → restaurant)", s == 200, s)

    # Вебхуки: ingress отдаёт /webhooks/payments напрямую payment. Если бы запрос шёл через gateway, был бы 404 ROUTE_NOT_FOUND.
    body = json.dumps({"eventId": "smoke-1", "orderId": "00000000-0000-4000-8000-0000000000aa",
                       "paymentId": "00000000-0000-4000-8000-0000000000bb", "status": "succeeded"}).encode()
    hj = {"Content-Type": "application/json"}
    for src, sec in (("gateway", GW_SECRET), ("pos", POS_SECRET)):
        path = f"/webhooks/payments/{src}"
        s, _, d = req("POST", path, hj, body)
        check(f"{path} без подписи → 401 INVALID_SIGNATURE (отвечает сам payment, мимо gateway)",
              s == 401 and jcode(d) == "INVALID_SIGNATURE", f"{s} {d[:100]!r}")
        s, _, d = req("POST", path, {**hj, "X-Signature": sign(sec, body)}, body)
        check(f"{path} с верной подписью, неизвестный платёж → 400 (подпись принята)", s == 400, f"{s} {d[:100]!r}")
        s, _, d = req("POST", path, {**hj, "X-Signature": sign(sec, body, int(time.time()) - 3600)}, body)
        check(f"{path} с просроченной подписью → 401", s == 401 and jcode(d) == "INVALID_SIGNATURE", f"{s} {d[:100]!r}")
        s, _, d = req("POST", path, {**hj, "X-Signature": sign(b"wrong-secret", body)}, body)
        check(f"{path} с чужим секретом → 401", s == 401, s)
    big = b'{"x":"' + b"a" * 100_000 + b'"}'
    s, _, d = req("POST", "/webhooks/payments/gateway", hj, big)
    check("вебхук 100 КБ → 413 (proxy-body-size 64k на ingress)", s == 413, f"{s} {d[:80]!r}")
    s, _, d = req("GET", "/webhooks/payments/gateway")
    check("GET на вебхук → не 200", s != 200, s)


# ---------------------------------------------------------------- C. лимиты
def burst(n, method, path, headers_fn, body=None, workers=60, source=None):
    def one(i):
        s, h, _ = req(method, path, headers_fn(i), body, source=source)
        return s, h
    with cf.ThreadPoolExecutor(workers) as ex:
        return list(ex.map(one, range(n)))


def pod_login():
    """Один неверный логин от настоящего pod'а внутри кластера: другой source-IP, чем у основного клиента."""
    cmd = ["run", f"alt-{int(time.time())}", "--rm", "-i", "--restart=Never", "--quiet", "--image=curlimages/curl:8.11.1", "--",
           "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-X", "POST", f"http://{args.ingress_svc}/auth/login",
           "-H", f"Host: {HOSTHDR}", "-H", "Content-Type: application/json",
           "-d", '{"phone":"+79990009998","password":"wrong-password"}']
    r = kubectl(*cmd)
    return r.stdout.strip()[-3:] or r.stderr.strip()[-200:]


def section_c():
    print("== C. лимиты (бакеты «сжигаются», секция последняя)")
    hj = {"Content-Type": "application/json"}
    body = b'{"eventId":"flood"}'
    rs = burst(600, "POST", "/webhooks/payments/gateway", lambda i: hj, body)
    codes = sorted({s for s, _ in rs})
    n503 = sum(1 for s, _ in rs if s in (503, 429))
    n401 = sum(1 for s, _ in rs if s == 401)
    check(f"ingress: 600 вебхуков за секунды → часть отсечена лимитом 503/429 ({n503}), остальные дошли до payment (401: {n401})",
          n503 > 0 and n401 > 0, f"коды: {codes}")

    login = json.dumps({"phone": "+79990009999", "password": "wrong-password"}).encode()
    rs = []
    for _ in range(args.auth_requests):
        s, h, d = req("POST", "/auth/login", hj, login)
        rs.append((s, h))
    n429 = sum(1 for s, _ in rs if s == 429)
    check(f"gateway: {args.auth_requests} неверных логинов с одного IP → 429 RATE_LIMITED ({n429}), до лимита — 401",
          n429 > 0 and rs[0][0] == 401, f"коды: {sorted({s for s, _ in rs})}")

    rs = burst(600, "GET", "/restaurants", lambda i: {})
    n429 = sum(1 for s, _ in rs if s == 429)
    ra = any(h.get("Retry-After") for s, h in rs if s == 429)
    check(f"gateway: всплеск GET /restaurants → 429 ({n429}) с Retry-After", n429 > 0 and ra, f"коды: {sorted({s for s, _ in rs})}")

    rs = burst(300, "GET", "/restaurants", lambda i: {"X-Forwarded-For": "203.0.113.77"})
    check("постоянный поддельный X-Forwarded-For не снимает лимит (ingress переписывает заголовок на реальный адрес)",
          any(s == 429 for s, _ in rs), f"коды: {sorted({s for s, _ in rs})}")
    rs = burst(200, "GET", "/restaurants", lambda i: {"X-Forwarded-For": f"198.51.100.{i % 250 + 1}"})
    check("ротация поддельных X-Forwarded-For не обходит лимит", any(s == 429 for s, _ in rs), f"коды: {sorted({s for s, _ in rs})}")

    # Другой реальный клиент: его IP для gateway другой, значит бакет свой.
    if args.alt_source:
        s, _, d = req("POST", "/auth/login", hj, login, source=args.alt_source)
        who = f"source {args.alt_source}"
    elif not args.no_kubectl:
        s, who = (int(x) if (x := pod_login()).isdigit() else 0), "pod в кластере"
    else:
        print("  skip  другой клиент: нужен kubectl или --alt-source")
        return
    check(f"другой реальный клиент ({who}) лимитом первого не затронут → 401 INVALID_CREDENTIALS, не 429", s == 401, s)


section_a() if not args.no_kubectl else print("== A. пропущена (--no-kubectl)")
section_b()
if not args.skip_limits:
    section_c()
print(f"\nитого: {sum(results)}/{len(results)} проверок")
sys.exit(0 if all(results) else 1)
