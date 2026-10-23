#!/bin/sh
# Nightly database dump into ./backups, keeping the last 14 days.
#   0 3 * * * cd /srv/fundus && ./deploy/backup.sh
# Attachments are plain files in ./data; back that directory up as well.
set -eu
cd "$(dirname "$0")/.."
stamp=$(date +%Y-%m-%d_%H%M)
docker compose exec -T db sh -c "pg_dump -U fundus -d fundus --format=custom --file=/backups/fundus-$stamp.dump"
find backups -name 'fundus-*.dump' -mtime +14 -delete
echo "backups/fundus-$stamp.dump"
# Restore:
#   docker compose stop fundus
#   docker compose exec -T db pg_restore -U fundus -d fundus --clean --if-exists /backups/fundus-<stamp>.dump
#   docker compose start fundus
