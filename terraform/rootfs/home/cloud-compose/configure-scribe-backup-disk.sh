#!/usr/bin/env bash

set -Eeuo pipefail

readonly DEVICE_LINK="/dev/disk/by-id/google-scribe-mariadb-backups"
readonly MOUNT_PATH="/mnt/disks/backups"
readonly FSTAB_PATH="/etc/fstab"
readonly BEGIN_MARKER="# BEGIN SCRIBE MARIADB BACKUPS"
readonly END_MARKER="# END SCRIBE MARIADB BACKUPS"

[[ "$(id -u)" -eq 0 ]] || { echo "backup disk configuration must run as root" >&2; exit 1; }

for _ in $(seq 1 180); do
  [[ -b "$DEVICE_LINK" ]] && break
  sleep 2
done
[[ -b "$DEVICE_LINK" ]] || { echo "Scribe backup disk did not attach within six minutes" >&2; exit 1; }

/home/cloud-compose/prepare-filesystem.sh "$DEVICE_LINK" "$MOUNT_PATH"
mountpoint -q -- "$MOUNT_PATH"
[[ ! -L "$MOUNT_PATH" && -d "$MOUNT_PATH" ]] || { echo "unsafe backup mount path" >&2; exit 1; }

device="$(readlink -f -- "$DEVICE_LINK")"
mounted_device="$(findmnt -rn -o SOURCE --target "$MOUNT_PATH")"
[[ "$(readlink -f -- "$mounted_device")" == "$device" ]] || { echo "unexpected backup mount source" >&2; exit 1; }
[[ "$(findmnt -rn -o FSTYPE --target "$MOUNT_PATH")" == "ext4" ]] || { echo "backup disk must use ext4" >&2; exit 1; }
uuid="$(blkid -s UUID -o value -- "$device")"
[[ "$uuid" =~ ^[A-Fa-f0-9-]{8,64}$ ]] || { echo "backup disk has no safe filesystem UUID" >&2; exit 1; }

if grep -Fq "$BEGIN_MARKER" "$FSTAB_PATH"; then
  grep -Fq "$END_MARKER" "$FSTAB_PATH" || { echo "unterminated Scribe backup fstab block" >&2; exit 1; }
  grep -Fq "UUID=${uuid}" "$FSTAB_PATH" || { echo "Scribe backup fstab UUID changed unexpectedly" >&2; exit 1; }
else
  if findmnt -sno TARGET | grep -Fxq "$MOUNT_PATH"; then
    echo "an unmanaged fstab entry already owns $MOUNT_PATH" >&2
    exit 1
  fi
  tmp="$(mktemp "${FSTAB_PATH}.scribe.XXXXXX")"
  trap 'rm -f -- "${tmp:-}"' EXIT
  cp --preserve=mode,ownership -- "$FSTAB_PATH" "$tmp"
  {
    printf '%s\n' "$BEGIN_MARKER"
    printf 'UUID=%s\t%s\text4\tdefaults,nofail,x-systemd.device-timeout=360s\t0\t2\n' "$uuid" "$MOUNT_PATH"
    printf '%s\n' "$END_MARKER"
  } >>"$tmp"
  mv -f -- "$tmp" "$FSTAB_PATH"
  trap - EXIT
fi

install -d -m 0750 -o cloud-compose -g cloud-compose "$MOUNT_PATH/mariadb"
systemctl daemon-reload
