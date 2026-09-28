#!/usr/bin/env bash
set -Eeuo pipefail

# Host-side rollback for the app-only NewAPI cutover. It deliberately does not
# call the Codex/NewAPI API: a broken API path must not remove the rollback path.
# Dry-run is the default; use --apply only after reviewing the printed paths.

BACKUP_DIR=""
APPLY=0
START_SCRIPT=/usr/local/sbin/newapi-start.sh
UNIT=newapi.service

usage() {
  printf '%s\n' "Usage: $0 --backup-dir /opt/newapi/backups/<snapshot> [--apply]"
}

while (($#)); do
  case "$1" in
    --backup-dir)
      (($# >= 2)) || { usage >&2; exit 2; }
      BACKUP_DIR=$2
      shift 2
      ;;
    --apply)
      APPLY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$BACKUP_DIR" || "$BACKUP_DIR" != /opt/newapi/backups/* ]]; then
  echo "backup-dir must be an existing child of /opt/newapi/backups" >&2
  exit 2
fi

SAVED_START="$BACKUP_DIR/newapi-start.sh.before"
if [[ ! -f "$SAVED_START" ]]; then
  echo "missing rollback pointer: $SAVED_START" >&2
  exit 1
fi

if ((EUID != 0)); then
  echo "rollback must run as root" >&2
  exit 1
fi

CURRENT_SHA=$(sha256sum "$START_SCRIPT" 2>/dev/null | awk '{print $1}' || true)
SAVED_SHA=$(sha256sum "$SAVED_START" | awk '{print $1}')
printf 'rollback_target=%s\ncurrent_start_sha256=%s\nsaved_start_sha256=%s\napply=%s\n' \
  "$BACKUP_DIR" "${CURRENT_SHA:-missing}" "$SAVED_SHA" "$APPLY"

if ((APPLY == 0)); then
  echo "dry-run only; no service or file was changed"
  exit 0
fi

STAMP=$(date -u +%Y%m%dT%H%M%SZ)
EMERGENCY_DIR="/opt/newapi/backups/rollback-pre-${STAMP}"
install -d -m 700 "$EMERGENCY_DIR"
if [[ -f "$START_SCRIPT" ]]; then
  install -m 755 "$START_SCRIPT" "$EMERGENCY_DIR/newapi-start.sh.before"
fi
printf 'timestamp=%s\nold_start_sha256=%s\nsaved_start_sha256=%s\n' \
  "$STAMP" "${CURRENT_SHA:-missing}" "$SAVED_SHA" > "$EMERGENCY_DIR/rollback-audit.txt"

install -m 755 "$SAVED_START" "$START_SCRIPT"
if ! systemctl restart "$UNIT"; then
  if [[ -f "$EMERGENCY_DIR/newapi-start.sh.before" ]]; then
    install -m 755 "$EMERGENCY_DIR/newapi-start.sh.before" "$START_SCRIPT"
  fi
  echo "newapi restart failed; original pointer restored" >&2
  exit 1
fi

for _ in $(seq 1 45); do
  if curl --fail --silent --show-error --max-time 2 http://127.0.0.1:3000/api/status >/dev/null; then
    echo "rollback_health=ok"
    exit 0
  fi
  sleep 2
done

echo "rollback_health=failed; restoring original pointer" >&2
if [[ -f "$EMERGENCY_DIR/newapi-start.sh.before" ]]; then
  install -m 755 "$EMERGENCY_DIR/newapi-start.sh.before" "$START_SCRIPT"
fi
systemctl restart "$UNIT" || true
exit 1
