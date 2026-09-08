"""Bounded, non-destructive HTTP checks for an explicitly authorized deployment.

Usage: python scripts/security-probe.py http://192.168.1.19
Does not create accounts, upload files, change data, or load-test the service.
"""
import argparse
import json
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base_url")
    args = parser.parse_args()
    base = args.base_url.rstrip("/")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    cases = [
        ("health", "/health/ready", "GET", None, {}, {200}),
        ("anonymous profile", "/api/v1/me", "GET", None, {}, {401}),
        ("forged session", "/api/v1/me", "GET", None, {"Cookie": "pan_session=forged"}, {401}),
        ("anonymous files", "/api/v1/nodes", "GET", None, {}, {401}),
        ("anonymous members", "/api/v1/members", "GET", None, {}, {401}),
        ("anonymous audit", "/api/v1/admin/audit", "GET", None, {}, {401}),
        ("foreign origin", "/api/v1/auth/login", "POST", b"{}", {"Origin": "https://evil.invalid"}, {403}),
        ("malformed share token", "/api/v1/public/shares/not-a-token", "GET", None, {}, {401, 404}),
        ("environment file", "/.env", "GET", None, {}, {403, 404}),
        ("git metadata", "/.git/config", "GET", None, {}, {403, 404}),
        ("source traversal", "/%2e%2e/%2e%2e/etc/passwd", "GET", None, {}, {400, 403, 404}),
        ("unsigned object", "/pan-objects/security-probe-missing", "GET", None, {}, {403, 404}),
        ("object delete blocked", "/pan-objects/security-probe-missing", "DELETE", None, {}, {405}),
    ]
    results = []
    for label, path, method, data, headers, expected in cases:
        request = urllib.request.Request(base + path, data=data, method=method,
                                         headers={"Content-Type": "application/json", **headers})
        try:
            response = opener.open(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read(2048)
            healthy = True
            if label == "health":
                try:
                    health = json.loads(payload)
                    healthy = isinstance(health, dict) and health.get("status") == "ready"
                except (ValueError, UnicodeDecodeError):
                    healthy = False
            results.append({"test": label, "status": response.status,
                            "passed": response.status in expected and healthy})
    print(json.dumps({"target": base, "results": results}, indent=2))
    return 0 if all(item["passed"] for item in results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
