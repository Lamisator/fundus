#!/bin/sh
# Ship the committed source to a Docker host and (re)build the stack there.
#
#   deploy/deploy.sh root@k-inf1-server.kloeck-it.de /srv/docker/fundus
#
# The remote directory keeps its .env, data/, pgdata/ and backups/; only the
# source is replaced. Uses the Traefik overlay (deploy/compose.traefik.yml).
set -eu
host=${1:?usage: deploy.sh user@host /remote/dir}
dir=${2:?usage: deploy.sh user@host /remote/dir}
ssh_key=${SSH_KEY:-}
SSH="ssh ${ssh_key:+-i $ssh_key}"
cd "$(dirname "$0")/.."
version=$(git describe --tags --always --dirty)
echo "deploying $version to $host:$dir"
git archive --format=tar HEAD | $SSH "$host" "set -e
  mkdir -p '$dir/src.new' && tar -x -C '$dir/src.new'
  cd '$dir'
  for f in src.new/*; do n=\$(basename \$f); rm -rf \"\$n\"; mv \"\$f\" .; done
  for f in src.new/.[!.]*; do [ -e \"\$f\" ] || continue; n=\$(basename \$f); rm -rf \"\$n\"; mv \"\$f\" .; done
  rmdir src.new
  test -f .env || { echo 'no .env in $dir - create it from .env.example first'; exit 1; }
  sed -i 's/^FUNDUS_VERSION=.*//' .env; echo 'FUNDUS_VERSION=$version' >> .env
  docker compose -f docker-compose.yml -f deploy/compose.traefik.yml up -d --build --remove-orphans
  docker image prune -f >/dev/null
  docker compose ps"
