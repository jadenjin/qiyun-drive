#!/usr/bin/env python3
"""Write a small, non-secret host health report for the read-only API mount."""
import json
from pathlib import Path
import shutil
import subprocess
import time

root = Path("/opt/qiyun-drive/ops")
root.mkdir(parents=True, exist_ok=True, mode=0o755)
disks = []
for label, path in (("系统盘", "/"), ("数据盘", "/mnt/data"), ("网盘对象存储", "/srv/qiyun-drive-rustfs")):
    if Path(path).exists():
        usage = shutil.disk_usage(path)
        disks.append({"label": label, "totalBytes": usage.total, "usedBytes": usage.used, "freeBytes": usage.free})
services = []
for service in ("postgres", "rustfs", "api", "worker", "web", "caddy"):
    result = subprocess.run(["docker", "inspect", "--format", "{{json .State}}", "qiyun-drive-" + service + "-1"], capture_output=True, timeout=15)
    if result.returncode:
        services.append({"name": service, "status": "unavailable"})
        continue
    state = json.loads(result.stdout)
    services.append({"name": service, "status": state["Status"], "health": state.get("Health", {}).get("Status"), "startedAt": state["StartedAt"]})
report = {"updatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "disks": disks, "services": services}
temporary = root / "host-health.json.tmp"
temporary.write_text(json.dumps(report, ensure_ascii=False) + "\n")
temporary.chmod(0o644)
temporary.replace(root / "host-health.json")
