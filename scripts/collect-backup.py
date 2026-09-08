#!/usr/bin/env python3
"""Collect and verify the latest device snapshot using a known SSH host key."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import time
import paramiko


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--target", required=True, type=Path)
    parser.add_argument("--known-hosts", required=True)
    args = parser.parse_args()
    target = args.target.resolve(strict=True)
    base = PurePosixPath("/opt/qiyun-drive/backups")
    client = paramiko.SSHClient()
    client.load_host_keys(args.known_hosts)
    client.connect("192.168.1.19", username="root", password=os.environ.pop("QIYUN_SSH_PASSWORD"),
                   look_for_keys=False, allow_agent=False, timeout=15)
    lock_input = None
    try:
        # Keep the selected snapshot protected from host retention until copied.
        lock_input, lock_output, _ = client.exec_command(
            "flock -s -w 30 /opt/qiyun-drive/backups/.backup.lock sh -c 'echo locked; cat >/dev/null'", timeout=40)
        if lock_output.readline().strip() != "locked":
            raise RuntimeError("Backup is busy; collection will retry on the next schedule")
        with client.open_sftp() as sftp:
            with sftp.open(str(base / "latest")) as source:
                remote = PurePosixPath(source.read(4096).decode().strip())
            if remote.parent != base or not re.fullmatch(r"qiyun-\d{8}-\d{6}-\d+", remote.name):
                raise ValueError("Invalid latest backup path")
            folder = target / remote.name
            folder.mkdir(exist_ok=True)
            if folder.is_symlink() or folder.resolve().parent != target:
                raise ValueError("Invalid destination directory")
            with sftp.open(str(remote / "SHA256SUMS")) as source:
                manifest = source.read(65537).decode()
            if len(manifest) > 65536:
                raise ValueError("Checksum manifest is too large")
            entries = {}
            for line in manifest.splitlines():
                digest, name = line.split(maxsplit=1)
                name = name.removeprefix("./")
                if not re.fullmatch(r"[a-zA-Z0-9._-]+", name) or name in (".", "..") or not re.fullmatch(r"[a-f0-9]{64}", digest) or name in entries:
                    raise ValueError("Invalid checksum manifest")
                entries[name] = digest
            required = {"postgres.dump", "rustfs-data.tar.gz", "source.tar.gz", "images.tar.gz", "manifest.json", "images.txt", "compose.resolved.yaml"}
            required.update(service + ".env" for service in ("postgres", "rustfs", "api", "worker", "web", "caddy"))
            if not required.issubset(entries):
                raise ValueError("Backup artifacts are incomplete")
            for name, digest in entries.items():
                path = folder / name
                if path.is_symlink():
                    raise ValueError("Unexpected destination symlink")
                if path.is_file() and sha256(path) == digest:
                    continue
                temporary = folder / (name + ".part")
                if temporary.is_symlink():
                    raise ValueError("Unexpected temporary symlink")
                sftp.get(str(remote / name), str(temporary), max_concurrent_prefetch_requests=16)
                if sha256(temporary) != digest:
                    raise ValueError("Transferred backup failed checksum validation")
                temporary.replace(path)
            (folder / "SHA256SUMS").write_text(manifest)
            metadata = json.loads((folder / "manifest.json").read_text())
            report = {"status": "success", "backupCreatedAt": metadata["createdAt"],
                      "verifiedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                      "files": len(entries), "destination": "Windows off-host copy"}
            (folder / "offhost-verification.json").write_text(json.dumps(report) + "\n")
            latest = target / "latest.tmp"
            latest.write_text(str(folder))
            latest.replace(target / "latest")
            with sftp.open("/opt/qiyun-drive/ops/offhost-backup.json.tmp", "w") as output:
                output.write(json.dumps(report) + "\n")
            sftp.chmod("/opt/qiyun-drive/ops/offhost-backup.json.tmp", 0o644)
            sftp.posix_rename("/opt/qiyun-drive/ops/offhost-backup.json.tmp", "/opt/qiyun-drive/ops/offhost-backup.json")
            print(json.dumps(report), flush=True)
    finally:
        if lock_input is not None:
            lock_input.channel.shutdown_write()
        client.close()


if __name__ == "__main__":
    main()
