#!/usr/bin/env python3
"""Fill a Fundus instance with demo content through the REST API.

    FUNDUS_URL=http://localhost:8080 FUNDUS_TOKEN=fdk_... python3 tools/seed-demo.py

The token needs the write and admin scopes (admin is used to create spaces).
"""
import json
import os
import sys
import urllib.request

BASE = os.environ.get("FUNDUS_URL", "http://localhost:8080").rstrip("/") + "/api/v1/"
TOKEN = os.environ.get("FUNDUS_TOKEN") or sys.exit("set FUNDUS_TOKEN")


def call(method, path, body=None):
    req = urllib.request.Request(BASE + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Authorization": "Bearer " + TOKEN, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req) as r:
            data = r.read()
            return json.loads(data) if data else None
    except urllib.error.HTTPError as e:
        sys.exit(f"{method} {path}: {e.code} {e.read().decode()}")


spaces = {s["slug"]: s["id"] for s in call("GET", "spaces")}
for slug, name, desc, icon, color in [
    ("homelab", "Homelab", "Servers, network and services at home.", "home", "#12a594"),
    ("work", "Work", "Company infrastructure and tooling.", "monitor", "#3e63dd"),
    ("desktop", "Desktop & Laptop", "Workstations, macOS, Windows, Linux desktops.", "cpu", "#f76b15"),
]:
    if slug not in spaces:
        spaces[slug] = call("POST", "spaces", {"slug": slug, "name": name, "description": desc, "icon": icon, "color": color})["id"]

E = []


def entry(**kw):
    sp = kw.pop("space", None)
    if sp:
        kw["space_id"] = spaces[sp]
    e = call("POST", "entries", kw)
    E.append(e)
    return e["id"]


docker = entry(kind="problem", space="homelab", title="Docker: permission denied while trying to connect to /var/run/docker.sock",
    summary="Every docker command fails for a normal user after a fresh install.",
    error_text="permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: Get \"http://%2Fvar%2Frun%2Fdocker.sock/v1.47/containers/json\": dial unix /var/run/docker.sock: connect: permission denied",
    tags=["docker", "linux", "permissions"], fields={"os": "Debian 13", "software": "Docker CE", "version": "29.0", "host": "k-inf1"},
    body="## Symptoms\n\n`docker ps` works with `sudo` but fails as my user.\n\n## Root cause\n\nThe socket is owned by `root:docker` with mode `660`, and the user was not in the `docker` group.\n\n> [!WARNING]\n> Membership in `docker` is equivalent to root on that host.\n",
    solutions=[{"title": "Add the user to the docker group", "accepted": True,
                "body": "```sh\nsudo usermod -aG docker $USER\nnewgrp docker   # or log out and back in\ndocker ps\n```"},
               {"title": "Use rootless Docker", "body": "Install `docker-ce-rootless-extras` and run `dockerd-rootless-setuptool.sh install`. Then `export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/docker.sock`."}])

entry(kind="problem", space="work", title="Postgres: FATAL: sorry, too many clients already",
    summary="The API started returning 500s under load; Postgres refused new connections.",
    error_text="FATAL:  sorry, too many clients already", tags=["postgres", "database", "performance"],
    fields={"software": "PostgreSQL", "version": "17.6", "host": "db-prod-1"},
    body="## Symptoms\n\n- Spikes of HTTP 500 at peak times\n- `pg_stat_activity` shows ~100 idle connections from the app\n\n## Root cause\n\nEach replica opened its own pool of 25 connections, 5 replicas × 25 > `max_connections = 100`.\n",
    solutions=[{"title": "Put PgBouncer in front and shrink the pools", "accepted": True,
                "body": "1. Run PgBouncer in `transaction` mode.\n2. Point `DATABASE_URL` at PgBouncer.\n3. Reduce the per-replica pool to 10.\n\n```ini\n[pgbouncer]\npool_mode = transaction\nmax_client_conn = 500\ndefault_pool_size = 40\n```"}])

wifi = entry(kind="problem", space="desktop", title="Wi-Fi drops every few minutes on Intel AX211 (Linux)",
    summary="iwlwifi disconnects periodically, reconnects after ~10 s.", status="investigating",
    error_text="iwlwifi 0000:00:14.3: Microcode SW error detected. Restarting 0x0.", tags=["linux", "wifi", "kernel"],
    fields={"os": "Fedora 44", "software": "iwlwifi", "version": "kernel 6.17", "host": "ThinkPad X1"},
    body="## Symptoms\n\nConnection drops roughly every 5 minutes, `dmesg` shows the microcode error.\n\n## Tried so far\n\n- [x] Disable power saving: `options iwlwifi power_save=0`\n- [x] Newer `linux-firmware`\n- [ ] Disable 802.11ax: `options iwlwifi disable_11ax=Y`\n")

entry(kind="problem", space="work", title="git push rejected: remote: error: GH013: Repository rule violations found",
    error_text="remote: error: GH013: Repository rule violations found for refs/heads/main.\nremote: - Changes must be made through a pull request.",
    tags=["git", "github"], fields={"software": "GitHub"}, status="solved",
    body="Pushing straight to `main` is blocked by a ruleset.",
    solutions=[{"accepted": True, "body": "Push a branch and open a PR:\n\n```sh\ngit switch -c fix/thing\ngit push -u origin fix/thing\ngh pr create --fill\n```"}])

entry(kind="problem", space="homelab", title="nginx: 413 Request Entity Too Large on file uploads",
    summary="Uploads over 1 MB fail behind the reverse proxy.", error_text="413 Request Entity Too Large\nnginx/1.27.2",
    tags=["nginx", "http", "uploads"], fields={"software": "nginx", "version": "1.27.2"},
    body="nginx defaults to `client_max_body_size 1m`. See [[%d]] for a related permission issue on the same host." % docker,
    solutions=[{"title": "Raise client_max_body_size", "accepted": True, "body": "```nginx\nserver {\n    client_max_body_size 100m;\n}\n```\nThen `nginx -t && systemctl reload nginx`."}])

entry(kind="problem", space="desktop", title="macOS: “App is damaged and can’t be opened”",
    error_text="“App” is damaged and can’t be opened. You should move it to the Trash.", tags=["macos", "gatekeeper"],
    fields={"os": "macOS 26"}, solutions=[{"accepted": True, "body": "Remove the quarantine attribute (only for apps you trust):\n\n```sh\nxattr -dr com.apple.quarantine /Applications/App.app\n```"}])

entry(kind="problem", space="work", title="Kubernetes pod stuck in CrashLoopBackOff after ConfigMap change", status="open",
    error_text="Back-off restarting failed container app in pod api-7d9f8c6b5-x2kq4", tags=["kubernetes", "k8s"],
    fields={"software": "Kubernetes", "version": "1.34"},
    body="## Symptoms\n\nAfter changing the `api-config` ConfigMap the new pods crash immediately.\n\n## Next steps\n\n- `kubectl logs --previous`\n- compare the rendered config with the old one\n")

runbook = entry(kind="runbook", space="homelab", title="Restore Postgres from a nightly dump", status="current",
    tags=["postgres", "backup", "restore"], fields={"service": "PostgreSQL", "host": "k-inf1"}, pinned=True,
    summary="From backup file to a running database in ten minutes.",
    body="## When to use\n\nData loss or corruption on the main database.\n\n## Procedure\n\n1. Stop the app: `docker compose stop app`\n2. Drop and recreate the database:\n\n```sh\ndocker compose exec postgres dropdb -U app app\ndocker compose exec postgres createdb -U app app\n```\n\n3. Restore:\n\n```sh\ngunzip -c backups/app-2026-10-22.sql.gz | docker compose exec -T postgres psql -U app app\n```\n\n4. Start the app and check `/healthz`.\n\n## Rollback\n\nKeep the broken data directory (`mv pgdata pgdata.broken`) until the restore is verified.\n")

entry(kind="howto", space="homelab", title="Issue a wildcard Let’s Encrypt certificate with Traefik and DNS-01",
    tags=["traefik", "tls", "letsencrypt"], fields={"software": "Traefik", "version": "Tested with 3.6"},
    body="## Goal\n\n`*.example.com` certificate without opening port 80.\n\n## Steps\n\n1. Create an API token at your DNS provider.\n2. Configure the resolver:\n\n```yaml\ncertificatesResolvers:\n  le:\n    acme:\n      email: admin@example.com\n      storage: /acme/acme.json\n      dnsChallenge:\n        provider: cloudflare\n```\n\n3. Add `tls.domains[0].main=example.com` and `sans=*.example.com` labels.\n\n> [!TIP]\n> Use the staging CA while testing to avoid rate limits.\n")

entry(kind="snippet", title="Find the 20 biggest directories", tags=["linux", "disk", "shell"], fields={"language": "bash"},
    body="```bash\ndu -xh / 2>/dev/null | sort -rh | head -20\n```\n\nOr interactively: `ncdu -x /`.")

entry(kind="snippet", title="List listening ports with the owning process", tags=["linux", "network", "shell"], fields={"language": "bash"},
    body="```bash\nsudo ss -tulpn\n```\n\n| Flag | Meaning |\n| ---- | ------- |\n| `-t` | TCP |\n| `-u` | UDP |\n| `-l` | listening |\n| `-p` | process |\n| `-n` | numeric |\n")

entry(kind="reference", title="PostgreSQL connection pooling (PgBouncer docs)", tags=["postgres", "database"],
    fields={"url": "https://www.pgbouncer.org/config.html"}, summary="Pool modes and the settings that matter.",
    body="- `transaction` mode breaks session features such as `SET`, advisory locks and prepared statements (unless `max_prepared_statements` is set).\n- Size `default_pool_size` against `max_connections`.")

entry(kind="note", space="work", title="On-call handover notes, week 43", tags=["oncall"],
    body="- Disk on `db-prod-1` at 78 %%, cleanup ticket open\n- Certificate for `api.example.com` renews on 2 Nov\n- Wi-Fi issue on the X1 still open, see [[%d]]" % wifi)

call("POST", f"entries/{runbook}/links", {"to": E[1]["id"], "rel": "related"})
call("POST", f"entries/{E[4]['id']}/links", {"to": docker, "rel": "related"})
print(f"created {len(E)} entries")
