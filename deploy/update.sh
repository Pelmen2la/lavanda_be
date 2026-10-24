#!/usr/bin/env bash
# Installed deployment: Ubuntu, systemd, PostgreSQL and static Next.js export.
set -Eeuo pipefail
umask 077

mode=${1:-all}
if [[ $# -gt 1 ]] || [[ ! $mode =~ ^(all|frontend|backend)$ ]]; then
    echo 'Usage: lavanda-update [all|frontend|backend]' >&2
    exit 2
fi
if [[ $EUID -ne 0 ]]; then
    echo 'Run as root: sudo lavanda-update [all|frontend|backend]' >&2
    exit 1
fi

be=/root/lavanda-src/lavanda_be
fe=/root/lavanda-src/lavanda_fe
app=/opt/lavanda
web=/var/www/lavanda
backup_root=/var/backups/lavanda
service=lavanda
phase=preflight
stopped=0
backup=''

failed() {
    local status=$?
    trap - EXIT
    if (( status != 0 )); then
        echo "Update failed during: $phase. No further changes will be applied." >&2
        [[ -z $backup ]] || echo "Backup: $backup" >&2
        if (( stopped )); then
            echo 'API may be stopped. Inspect the migration output and journalctl -u lavanda -n 100 before restarting.' >&2
            echo 'Database and binary are not rolled back automatically.' >&2
        fi
    fi
    exit "$status"
}
trap failed EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for command in git flock install tar cp mv find chown chmod curl date; do
    command -v "$command" >/dev/null || { echo "Missing command: $command" >&2; exit 1; }
done
exec 9>/run/lock/lavanda-update.lock
flock -n 9 || { echo 'Another Lavanda update is already running.' >&2; exit 1; }

check_repo() {
    local repo=$1
    [[ $(git -C "$repo" branch --show-current) == master ]] || { echo "Expected master branch: $repo" >&2; exit 1; }
    [[ -z $(git -C "$repo" status --porcelain) ]] || { echo "Local changes in $repo. Save or review them before updating; nothing was reset." >&2; exit 1; }
}

if [[ $mode != frontend ]]; then
    for command in go sudo systemctl pg_dump; do
        command -v "$command" >/dev/null || { echo "Missing command: $command" >&2; exit 1; }
    done
    check_repo "$be"
    [[ -x $app/lavanda-api && -f /etc/lavanda.env ]] || { echo 'Existing API installation or configuration not found.' >&2; exit 1; }
    if [[ ! -e $app/.env && ! -L $app/.env ]]; then ln -s /etc/lavanda.env "$app/.env"; fi
    [[ $(readlink -f "$app/.env") == /etc/lavanda.env ]] || { echo '/opt/lavanda/.env must point to /etc/lavanda.env.' >&2; exit 1; }
    sudo -u lavanda test -r "$app/.env"
    systemctl is-active --quiet "$service" || { echo 'API is not running. Diagnose it before deployment.' >&2; exit 1; }
fi
if [[ $mode != backend ]]; then
    command -v npm >/dev/null
    check_repo "$fe"
    [[ -d $web ]] || { echo "Website directory not found: $web" >&2; exit 1; }
fi

# Obtain and build everything before interrupting the running application.
phase='fetching sources'
if [[ $mode != frontend ]]; then git -C "$be" pull --ff-only origin master; fi
if [[ $mode != backend ]]; then git -C "$fe" pull --ff-only origin master; fi
if [[ $mode != frontend ]]; then
    phase='building backend'
    echo 'Building backend...'
    (cd "$be"; GOMAXPROCS=1 CGO_ENABLED=0 go build -p 1 -trimpath -ldflags='-s -w' -o bin/lavanda-api ./cmd/server)
fi
if [[ $mode != backend ]]; then
    phase='building frontend'
    echo 'Building frontend...'
    (cd "$fe"; npm ci; npm run build)
    [[ -s $fe/out/index.html && -d $fe/out/_next/static ]]
fi

phase='creating backups'
backup="$backup_root/$(date +%Y%m%d-%H%M%S)-$$"
install -d -m 700 "$backup"
printf 'Mode: %s\n' "$mode" > "$backup/versions.txt"
if [[ $mode != frontend ]]; then
    git -C "$be" rev-parse HEAD >> "$backup/versions.txt"
    cp -p "$app/lavanda-api" "$backup/lavanda-api"
    install -o root -g lavanda -m 750 "$be/bin/lavanda-api" "$app/lavanda-api.new"
fi
if [[ $mode != backend ]]; then
    git -C "$fe" rev-parse HEAD >> "$backup/versions.txt"
    tar -czf "$backup/frontend.tar.gz" -C "$web" .
fi

if [[ $mode != frontend ]]; then
    # Stop writes before taking the database snapshot. Migrate uses one transaction.
    phase='stopping API and backing up database'
    stopped=1
    systemctl stop "$service"
    sudo -u postgres pg_dump -Fc lavanda > "$backup/database.dump"
    [[ -s $backup/database.dump ]]
    phase='database migration'
    (cd "$app"; sudo -u lavanda ./lavanda-api.new migrate)
    phase='installing backend'
    mv "$app/lavanda-api.new" "$app/lavanda-api"
fi
if [[ $mode != backend ]]; then
    phase='publishing frontend'
    # Publish hashed assets first; retain old assets for already open tabs.
    install -d -m 755 "$web/_next"
    cp -a "$fe/out/_next/." "$web/_next/"
    chown -R root:www-data "$web/_next"
    find "$web/_next" -type d -exec chmod 755 {} +
    find "$web/_next" -type f -exec chmod 644 {} +
    cp -a "$fe/out/." "$web/"
    chown -R root:www-data "$web"
    find "$web" -type d -exec chmod 755 {} +
    find "$web" -type f -exec chmod 644 {} +
fi
if [[ $mode != frontend ]]; then
    phase='starting API'
    systemctl start "$service"
    stopped=0
fi
phase='health check'
if [[ $mode != frontend ]]; then
    healthy=0
    for attempt in {1..15}; do
        if curl --fail --silent --max-time 3 http://127.0.0.1:8080/api/health >/dev/null; then healthy=1; break; fi
        sleep 1
    done
    (( healthy )) || { echo 'API health check failed. Run: journalctl -u lavanda -n 100 --no-pager' >&2; exit 1; }
    systemctl is-active --quiet "$service"
fi
if [[ $mode != backend ]]; then
    curl --fail --silent --show-error --max-time 15 --output /dev/null https://lavanda-beauty.ru/
fi
echo "Update complete ($mode). Backup: $backup"
