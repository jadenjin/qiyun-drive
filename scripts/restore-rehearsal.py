#!/usr/bin/env python3
"""Restore a backup into isolated containers on a different filesystem.

Only a loopback port is published. Test credentials and restored data are
removed afterwards; the production project's containers and volumes are never
used as restore destinations.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tarfile
import tempfile
import time
import urllib.request


def run(*args, input=None):
    result = subprocess.run(args, input=input, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    if result.returncode:
        raise RuntimeError(f"Command failed: {args[0]} {args[1]} (exit {result.returncode})")
    return result.stdout.decode().strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("backup", type=Path)
    parser.add_argument("--work-root", type=Path, default=Path("/var/lib/qiyun-drive-restore"))
    parser.add_argument("--port", type=int, default=19080)
    parser.add_argument("--status-file", type=Path, default=Path("/opt/qiyun-drive/ops/restore-status.json"))
    args = parser.parse_args()
    args.started_at = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    args.status_file.parent.mkdir(parents=True, exist_ok=True)
    initial = {"status": "running", "startedAt": args.started_at, "updatedAt": args.started_at}
    temporary = args.status_file.with_suffix(".tmp")
    temporary.write_text(json.dumps(initial) + "\n")
    temporary.chmod(0o644)
    temporary.replace(args.status_file)
    try:
        perform(args)
    except Exception:
        initial.update(status="failed", updatedAt=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()))
        temporary.write_text(json.dumps(initial) + "\n")
        temporary.chmod(0o644)
        temporary.replace(args.status_file)
        raise


def perform(args):
    backup = args.backup.resolve(strict=True)
    args.work_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    work_root = args.work_root.resolve(strict=True)
    if not 1024 <= args.port <= 65535:
        raise ValueError("Invalid loopback test port")
    # Verify every artifact before using any configuration or image.
    subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=backup, check=True, stdout=subprocess.DEVNULL)
    manifest = json.loads((backup / "manifest.json").read_text())
    source = Path(manifest["objectSource"])
    if source.exists() and source.stat().st_dev == work_root.stat().st_dev:
        raise ValueError("Restore work root must use a different filesystem from the live object store")
    images = dict(line.split() for line in (backup / "images.txt").read_text().splitlines())
    services = ("postgres", "rustfs", "api", "worker", "web", "caddy")
    if set(images) != set(services) or any(not image.startswith("sha256:") for image in images.values()):
        raise ValueError("Invalid image manifest")
    if (backup / "images.tar.gz").exists():
        run("docker", "load", "-i", str(backup / "images.tar.gz"))
    root = Path(tempfile.mkdtemp(prefix="rehearsal-", dir=work_root)).resolve()
    prefix = "qiyun-restore-" + secrets.token_hex(5)
    containers = []
    network_created = False
    gateway_created = False
    started = args.started_at
    status = {"status": "running", "startedAt": started, "backupCreatedAt": manifest["createdAt"]}

    def write_status():
        args.status_file.parent.mkdir(parents=True, exist_ok=True)
        temporary = args.status_file.with_suffix(".tmp")
        temporary.write_text(json.dumps(status) + "\n")
        temporary.chmod(0o644)
        temporary.replace(args.status_file)

    def start(service, extra=(), command=(), memory="256m", user=None, network=None):
        name = prefix + "-" + service
        args_list = ["docker", "run", "-d", "--name", name, "--network", network or prefix, "--network-alias", service,
                     "--read-only", "--security-opt", "no-new-privileges:true", "--cap-drop", "ALL",
                     "--pids-limit", "128", "--memory", memory, "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=134217728",
                     "--env-file", str(backup / (service + ".env"))]
        if user:
            args_list += ["--user", user]
        containers.append(name)
        run(*args_list, *extra, images[service], *command)
        return name

    def wait(check, label):
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            try:
                check()
                return
            except Exception:
                time.sleep(1)
        raise RuntimeError("Restore readiness timeout: " + label)

    def sql(query):
        return run("docker", "exec", "-i", prefix + "-postgres", "psql", "-v", "ON_ERROR_STOP=1", "-At", "-U", "pan", "-d", "pan", input=query.encode())

    write_status()
    try:
        run("docker", "network", "create", "--internal", prefix)
        network_created = True
        # Only the Caddyfile is needed from the source bundle. Never extract
        # arbitrary paths or links from the configuration archive.
        with tarfile.open(backup / "source.tar.gz", "r:gz") as archive:
            member = archive.getmember("./deploy/Caddyfile")
            if not member.isfile() or member.size > 1_000_000:
                raise ValueError("Invalid Caddy configuration artifact")
            with archive.extractfile(member) as stream:
                (root / "Caddyfile").write_bytes(stream.read())
        (root / "Caddyfile").chmod(0o644)
        for directory, uid in (("postgres", 70), ("objects", 10001), ("caddy", 10002)):
            path = root / directory
            path.mkdir(mode=0o750)
            os.chown(path, uid, uid)
        subprocess.run(["tar", "-xzf", str(backup / "rustfs-data.tar.gz"), "-C", str(root / "objects")], check=True)
        pg = start("postgres", ("-v", f"{root / 'postgres'}:/var/lib/postgresql/data", "--tmpfs", "/var/run/postgresql:rw,nosuid,nodev,size=16777216,uid=70,gid=70,mode=3777"), memory="192m")
        wait(lambda: run("docker", "exec", pg, "pg_isready", "-h", "127.0.0.1", "-U", "pan", "-d", "pan"), "PostgreSQL")
        with (backup / "postgres.dump").open("rb") as dump:
            result = subprocess.run(["docker", "exec", "-i", pg, "pg_restore", "--exit-on-error", "--no-owner", "--no-privileges", "-U", "pan", "-d", "pan"], stdin=dump, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            if result.returncode:
                raise RuntimeError("Database restore failed")
        rustfs = start("rustfs", ("-v", f"{root / 'objects'}:/data", "--tmpfs", "/logs:rw,noexec,nosuid,nodev,size=67108864,uid=10001,gid=10001,mode=0750"), memory="384m")
        wait(lambda: run("docker", "exec", rustfs, "curl", "--fail", "--silent", "http://127.0.0.1:9000/health"), "RustFS")
        public = f"http://127.0.0.1:{args.port}"
        overrides = ("-e", "PUBLIC_BASE_URL=" + public, "-e", "ALLOWED_ORIGIN=" + public, "-e", "S3_PUBLIC_ENDPOINT=" + public,
                     "-e", "S3_ENDPOINT=http://rustfs:9000", "-e", "COOKIE_SECURE=false")
        start("api", overrides, ("pan-api",), memory="192m")
        start("worker", overrides, ("pan-worker",))
        start("web", ("-e", "PUBLIC_BASE_URL=" + public,))
        # Docker does not publish host ports for an internal-only network.
        # Only Caddy joins this second gateway network; data services have no
        # external network, and the test port remains bound to loopback.
        run("docker", "network", "create", prefix + "-gateway")
        gateway_created = True
        caddy = start("caddy", ("-p", f"127.0.0.1:{args.port}:80", "-e", "SITE_ADDRESS=:80", "--sysctl", "net.ipv4.ip_unprivileged_port_start=0",
                        "-v", f"{root / 'Caddyfile'}:/etc/caddy/Caddyfile:ro", "-v", f"{root / 'caddy'}:/data",
                        "--tmpfs", "/config:rw,nosuid,nodev,size=16777216,uid=10002,gid=10002,mode=0750"), memory="96m", network=prefix + "-gateway")
        run("docker", "network", "connect", prefix, caddy)

        def get(path, cookie=None):
            request = urllib.request.Request(public + path, headers={"Cookie": cookie} if cookie else {})
            with urllib.request.urlopen(request, timeout=20) as response:
                return response.read()

        def health():
            if json.loads(get("/health/ready")).get("status") != "ready":
                raise RuntimeError("Restore API not ready")
            if "栖云" not in get("/").decode():
                raise RuntimeError("Restore web page unavailable")

        wait(health, "full application")
        # Check stored originals against the restored object data, not against
        # production URLs. Use each file owner's session only in this clone.
        # A current backup includes the immutable digest of each original.
        has_digests = sql("SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='assets' AND column_name='sha256');") == "t"
        if not has_digests:
            raise RuntimeError("Backup predates integrity indexing; create a current backup before the full hash rehearsal")
        records = json.loads(sql("SELECT COALESCE(json_agg(t),'[]') FROM (SELECT n.id,CASE WHEN sp.kind='personal' THEN sp.owner_user_id ELSE (SELECT hm.user_id FROM household_members hm WHERE hm.household_id=sp.household_id AND hm.role='owner' LIMIT 1) END AS test_user_id,a.sha256,a.size_bytes FROM nodes n JOIN assets a ON a.id=n.asset_id JOIN spaces sp ON sp.id=n.space_id WHERE n.deleted_at IS NULL AND a.status='ready' ORDER BY n.created_at LIMIT 12) t;"))
        verified = 0
        for record in records:
            token = secrets.token_urlsafe(32)
            hashed = base64.urlsafe_b64encode(hashlib.sha256(token.encode()).digest()).decode().rstrip("=")
            sql(f"INSERT INTO sessions(id,user_id,token_hash,expires_at) VALUES(gen_random_uuid(),'{record['test_user_id']}','{hashed}',now()+interval '5 minutes');")
            info = json.loads(get("/api/v1/nodes/" + record["id"] + "/download", "pan_session=" + token))
            url = info["url"]
            if not url.startswith(public + "/"):
                raise RuntimeError("Restored download escaped the isolated endpoint")
            with urllib.request.urlopen(url, timeout=120) as response:
                digest = hashlib.sha256()
                size = 0
                while chunk := response.read(1024 * 1024):
                    digest.update(chunk)
                    size += len(chunk)
            if not record["sha256"]:
                raise RuntimeError("Original integrity indexing is incomplete")
            if size != record["size_bytes"] or digest.hexdigest() != record["sha256"]:
                raise RuntimeError("Restored file hash or size mismatch")
            verified += 1
        if records and not verified:
            raise RuntimeError("No restored files verified")
        status.update(status="success", verifiedFiles=verified, services=6)
    except Exception:
        status["status"] = "failed"
        raise
    finally:
        cleanup_failed = False
        for name in reversed(containers):
            removed = subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if removed.returncode:
                exists = subprocess.run(["docker", "inspect", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                cleanup_failed = cleanup_failed or exists.returncode == 0
        if network_created:
            removed = subprocess.run(["docker", "network", "rm", prefix], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            cleanup_failed = cleanup_failed or removed.returncode != 0
        if gateway_created:
            removed = subprocess.run(["docker", "network", "rm", prefix + "-gateway"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            cleanup_failed = cleanup_failed or removed.returncode != 0
        if not cleanup_failed and root.parent == work_root and root.name.startswith("rehearsal-"):
            shutil.rmtree(root)
        if cleanup_failed:
            status.update(status="failed", cleanupFailed=True)
        status["updatedAt"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        write_status()
        if status["status"] == "success":
            latest = args.status_file.with_name("restore-latest.json")
            temporary = latest.with_suffix(".tmp")
            temporary.write_text(json.dumps(status)+"\n")
            temporary.chmod(0o644)
            temporary.replace(latest)
    if status["status"] != "success":
        raise RuntimeError("Restore rehearsal did not complete cleanly")
    print(json.dumps(status))


if __name__ == "__main__":
    main()
