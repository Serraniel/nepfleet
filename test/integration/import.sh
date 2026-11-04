#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Importing a running Compose project and applying it must not recreate
# containers, and bind mounts must keep pointing at the existing data.
# Usage: import.sh <nepfleet-binary>   (needs sops and age-keygen)
set -euo pipefail
NF=$(realpath "$1"); T=$(mktemp -d); P=nepfleet_import_it
fail() { echo "FAIL: $*"; exit 1; }
cleanup() { docker compose -p $P down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT
mkdir -p "$T/legacy/html" && cd "$T/legacy"
echo "existing data" > html/index.html
printf 'services:\n  web:\n    image: nginx:1.29-alpine\n    environment: {GREETING: "${GREETING}", DB_PASSWORD: "${DB_PASSWORD}"}\n    volumes: ["./html:/usr/share/nginx/html:ro"]\n    healthcheck: {test: ["CMD", "wget", "-qO-", "http://127.0.0.1/"], interval: 2s}\n' > compose.yaml
printf 'GREETING=hello\nDB_PASSWORD=s3cr3t\n' > .env
docker compose -p $P up -d --wait >/dev/null 2>&1
before=$(docker compose -p $P ps -q web)

cd "$T" && age-keygen -o key.txt 2>/dev/null && export SOPS_AGE_KEY_FILE=$T/key.txt
"$NF" init fleet >/dev/null && cd fleet
sed -i "s#    age: \[\]#    age: [\"$(age-keygen -y ../key.txt)\"]#" fleet.yaml
"$NF" import compose --state "$T/state" --target local $P | tee ../import.log
grep -q 'secrets (encrypted): DB_PASSWORD' ../import.log || fail "DB_PASSWORD not treated as secret"
grep -q s3cr3t stacks/$P/stack.yaml stacks/$P/values.secret.yaml && fail "secret in plaintext"

grep -q 'adopted' ../import.log || fail "running state not adopted"
"$NF" plan --state "$T/state" $P | grep -q 'up to date' || fail "first plan after import is not empty"
"$NF" apply --yes --state "$T/state" $P | grep -q 'up to date' || fail "apply after import changed something"
after=$(docker compose -p $P ps -q web)
[ "$before" = "$after" ] || fail "container was recreated ($before -> $after)"
[ "$(docker exec "$after" cat /usr/share/nginx/html/index.html)" = "existing data" ] || fail "bind mount lost"
[ "$(docker exec "$after" printenv DB_PASSWORD)" = s3cr3t ] || fail "secret value lost"
echo PASS
