#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# End to end on the local Docker daemon: secrets, generated values, apply,
# health-gated rollback, history and git rollback.
# Usage: e2e.sh <nepfleet-binary> <templates-checkout>   (needs sops and age-keygen)
set -euo pipefail
NF=$(realpath "$1"); TPL=$(realpath "$2"); T=$(mktemp -d)
fail() { echo "FAIL: $*"; exit 1; }
cleanup() { for p in web registry; do docker compose -p $p down -v >/dev/null 2>&1 || true; done; docker network rm nepfleet_e2e >/dev/null 2>&1 || true; }
trap cleanup EXIT
cd "$T"
age-keygen -o key.txt 2>/dev/null; export SOPS_AGE_KEY_FILE=$T/key.txt
"$NF" init fleet >/dev/null && cd fleet
git config commit.gpgsign false; git config user.name e2e; git config user.email e2e@example.invalid
sed -i "s#      url: https://github.com/Serraniel/nepfleet-templates#      path: $TPL#; /      ref: main/d; s#    age: \[\]#    age: [\"$(age-keygen -y ../key.txt)\"]#; 0,/^spec:/s//spec:\n  ingressNetwork: nepfleet_e2e/" fleet.yaml
mkdir -p stacks/registry stacks/web
printf 'apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: registry}\nspec:\n  target: local\n  template: {source: official, name: registry}\n  values: {DOMAIN: registry.example.org}\n' > stacks/registry/stack.yaml
printf 'apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: web}\nspec:\n  target: local\n  apply: auto\n' > stacks/web/stack.yaml
printf 'services:\n  web:\n    image: nginx:1.29-alpine\n    healthcheck: {test: ["CMD", "wget", "-qO-", "http://127.0.0.1/"], interval: 2s, retries: 3}\n' > stacks/web/compose.yaml
printf '%s' 'alice:$2y$05$abcdefghijklmnopqrstuv' | "$NF" secret set --state "$T/state" registry HTPASSWD >/dev/null
grep -q alice stacks/registry/values.secret.yaml && fail "secret stored in plaintext"
git add -A && git commit -qm init

"$NF" plan --state "$T/state" | grep -q "HTPASSWD='••••••" || fail "secret not masked in plan"
"$NF" apply --yes --state "$T/state" | grep -c '✓ applied' | grep -qx 2 || fail "apply"
grep -q HTTP_SECRET <(SOPS_AGE_KEY_FILE=$T/key.txt sops decrypt stacks/registry/values.secret.yaml) || fail "generated secret not saved"
"$NF" plan --state "$T/state" | grep -c 'up to date' | grep -qx 2 || fail "re-plan not empty"

sed -i 's#"http://127.0.0.1/"#"http://127.0.0.1:9/"#' stacks/web/compose.yaml && git commit -qam "break web"
out=$("$NF" apply --state "$T/state" --wait 15s web 2>&1) && fail "apply of an unhealthy stack must exit non-zero"
grep -q "rolled back" <<<"$out" || fail "no rollback on unhealthy apply"
[ "$(docker compose -p web ps --format '{{.Health}}')" = healthy ] || fail "web not healthy after rollback"
"$NF" history --state "$T/state" web | grep -q '▶ .* init' || fail "history does not mark the applied version"

"$NF" rollback --state "$T/state" --yes web | grep -q 'up to date' || fail "git rollback"
git log -1 --format=%s | grep -q '^revert(web)' || fail "rollback commit missing"
echo PASS
