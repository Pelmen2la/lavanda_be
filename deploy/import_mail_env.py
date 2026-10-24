#!/usr/bin/env python3
"""Import only SMTP settings into an existing production environment file."""

import os
from pathlib import Path
import re
import sys
import tempfile

MAIL_KEYS = (
    "SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD", "SMTP_FROM",
    "ADMIN_NOTIFICATION_EMAIL",
)
ASSIGNMENT = re.compile(r"^\s*([A-Z_]+)\s*=(.*)$")


def merge_mail_settings(existing, incoming):
    values = {}
    for line in incoming.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        match = ASSIGNMENT.fullmatch(line)
        if not match or match[1] not in MAIL_KEYS:
            raise ValueError("The import file must contain only SMTP settings and ADMIN_NOTIFICATION_EMAIL")
        key, value = match.groups()
        if key in values:
            raise ValueError("Duplicate setting: " + key)
        values[key] = value
    for key in MAIL_KEYS[:5]:
        if not values.get(key, "").strip().strip("\"'"):
            raise ValueError("Missing setting: " + key)
    # Keep omitted optional settings and all unrelated configuration unchanged.
    kept = []
    for line in existing.splitlines():
        match = ASSIGNMENT.fullmatch(line)
        if not match or match[1] not in values:
            kept.append(line)
    return "\n".join(kept + [key + "=" + values[key] for key in MAIL_KEYS if key in values]) + "\n"


def import_mail_settings(source, target):
    target = target.resolve(strict=True)
    existing = target.read_text(encoding="utf-8-sig")
    merged = merge_mail_settings(existing, source.read_text(encoding="utf-8-sig"))
    metadata = target.stat()
    # The backup is private; never print credentials or evaluate them as shell code.
    backup_fd, backup_name = tempfile.mkstemp(prefix=target.name + ".before-mail-", dir=target.parent)
    with os.fdopen(backup_fd, "w", encoding="utf-8", newline="\n") as backup:
        backup.write(existing)
    temporary_fd, temporary_name = tempfile.mkstemp(prefix=target.name + ".new-", dir=target.parent)
    try:
        with os.fdopen(temporary_fd, "w", encoding="utf-8", newline="\n") as output:
            output.write(merged)
            output.flush()
            os.fsync(output.fileno())
        if hasattr(os, "chown"):
            os.chown(temporary_name, metadata.st_uid, metadata.st_gid)
        os.chmod(temporary_name, 0o640)
        os.replace(temporary_name, target)
    finally:
        if os.path.exists(temporary_name):
            os.unlink(temporary_name)
    return Path(backup_name)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("Usage: python3 deploy/import_mail_env.py /root/lavanda-mail.env")
    if os.geteuid() != 0:
        sys.exit("Run this command as root on the Ubuntu server")
    try:
        backup = import_mail_settings(Path(sys.argv[1]), Path("/etc/lavanda.env"))
    except (OSError, ValueError) as error:
        sys.exit("Mail settings were not imported: " + str(error))
    print("Mail settings updated. Backup: " + str(backup))
    print("Restart the API to apply: systemctl restart lavanda")
