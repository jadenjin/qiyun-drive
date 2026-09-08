#!/bin/sh
# Run on the Docker host. Configuration and image manifests contain secrets;
# umask and the destination directory deliberately limit access to the owner.
set -eu
umask 077
project=$(CDPATH= cd -- "${QIYUN_PROJECT_ROOT:-$(dirname "$0")/..}" && pwd -P)
destination=${QIYUN_BACKUP_ROOT:-"$project/backups"}
mkdir -p "$destination" "$project/ops"
chmod 755 "$project/ops"
destination=$(CDPATH= cd -- "$destination" && pwd -P)
chmod 700 "$destination"
exec 9>"$destination/.backup.lock"
flock -n 9 || { echo 'Another backup is running' >&2; exit 1; }
cd "$project"
compose() {
  if [ -f compose.device.yaml ]; then
    docker compose -f compose.yaml -f compose.production.yaml -f compose.device.yaml "$@"
  elif [ -f compose.production.yaml ]; then
    docker compose -f compose.yaml -f compose.production.yaml "$@"
  else docker compose -f compose.yaml "$@"; fi
}
stamp=$(date -u +%Y%m%d-%H%M%S)
stage="$destination/.incomplete-$stamp-$$"
complete="$destination/qiyun-$stamp-$$"
mkdir "$stage"
resume=''
started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
total_bytes=0
write_status() {
  printf '{"status":"%s","startedAt":"%s","updatedAt":"%s","bytes":%s}\n' "$1" "$started" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$total_bytes" > "$project/ops/backup-status.json.tmp"
  chmod 644 "$project/ops/backup-status.json.tmp"
  mv "$project/ops/backup-status.json.tmp" "$project/ops/backup-status.json"
}
finish() {
  result=$?
  trap - EXIT INT TERM
  if [ -n "$resume" ]; then
    # Values originate only from the fixed service allowlist below.
    # shellcheck disable=SC2086
    compose start $resume >/dev/null || result=1
  fi
  if [ "$result" -ne 0 ]; then write_status failed; echo 'Backup failed; inspect protected incomplete directory' >&2; fi
  exit "$result"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
write_status running
for service in postgres rustfs api worker web caddy; do
  container=$(compose ps -a -q "$service")
  [ -n "$container" ] || { echo "Missing service: $service" >&2; exit 1; }
  image=$(docker inspect --format '{{.Image}}' "$container")
  printf '%s %s\n' "$service" "$image" >> "$stage/images.txt"
  docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$container" > "$stage/$service.env"
  case "$service" in api|worker|caddy|rustfs)
    if [ "$(docker inspect --format '{{.State.Running}}' "$container")" = true ]; then resume="$resume $service"; fi;;
  esac
done
# Image compression is done before the brief write freeze.
if [ "${QIYUN_BACKUP_IMAGES:-1}" = 1 ]; then
  awk '{print $2}' "$stage/images.txt" | sort -u > "$stage/image-ids.txt"
  docker image save -o "$stage/images.tar" $(cat "$stage/image-ids.txt")
  gzip "$stage/images.tar"
fi
backup_exclude='./backups'
case "$destination" in "$project"/*) backup_exclude="./${destination#"$project"/}";; esac
tar --exclude="$backup_exclude" --exclude='./.git' --exclude='./node_modules' --exclude='./.runtime' --exclude='./.security-cache' --exclude='./dist' --exclude='./backups' --exclude='./ops' -czf "$stage/source.tar.gz" .
compose config > "$stage/compose.resolved.yaml"
if [ -n "$resume" ]; then
  # shellcheck disable=SC2086
  compose stop $resume >/dev/null
fi
compose exec -T postgres pg_dump -U pan -d pan -Fc --no-owner --no-privileges > "$stage/postgres.dump"
rustfs=$(compose ps -a -q rustfs)
source=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' "$rustfs")
[ -n "$source" ] && [ -d "$source" ] || { echo 'RustFS data mount missing' >&2; exit 1; }
tar -C "$source" -czf "$stage/rustfs-data.tar.gz" .
# Restart before slower validation and off-host transfer.
if [ -n "$resume" ]; then
  # shellcheck disable=SC2086
  compose start $resume >/dev/null
  resume=''
fi
tar -tzf "$stage/rustfs-data.tar.gz" >/dev/null
compose exec -T postgres pg_restore --list < "$stage/postgres.dump" >/dev/null
printf '{"formatVersion":2,"createdAt":"%s","objectSource":"%s"}\n' "$started" "$source" > "$stage/manifest.json"
(cd "$stage" && sha256sum ./*.dump ./*.gz ./*.env images.txt manifest.json compose.resolved.yaml > SHA256SUMS && sha256sum -c SHA256SUMS >/dev/null)
mv "$stage" "$complete"
printf '%s\n' "$complete" > "$destination/latest.tmp"
mv "$destination/latest.tmp" "$destination/latest"
total_bytes=$(du -sb "$complete" | awk '{print $1}')
write_status success
cp "$project/ops/backup-status.json" "$project/ops/backup-latest.json.tmp"
chmod 644 "$project/ops/backup-latest.json.tmp"
mv "$project/ops/backup-latest.json.tmp" "$project/ops/backup-latest.json"
python3 "$project/scripts/backup-retention.py" "$destination" --keep "${QIYUN_BACKUP_KEEP:-7}"
printf 'Backup complete: %s\n' "$complete"
