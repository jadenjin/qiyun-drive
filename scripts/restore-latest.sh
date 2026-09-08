#!/bin/sh
set -eu
umask 077
project=$(CDPATH= cd -- "${QIYUN_PROJECT_ROOT:-$(dirname "$0")/..}" && pwd -P)
destination=${QIYUN_BACKUP_ROOT:-"$project/backups"}
destination=$(CDPATH= cd -- "$destination" && pwd -P)
# A backup cannot prune the selected snapshot while the rehearsal uses it.
exec 9>"$destination/.backup.lock"
flock -n 9 || { echo 'A backup or restore rehearsal is already running' >&2; exit 1; }
backup=$(cat "$destination/latest")
case "$backup" in "$destination"/qiyun-*) ;; *) echo 'Invalid latest backup path' >&2; exit 1;; esac
exec python3 "$project/scripts/restore-rehearsal.py" "$backup"
