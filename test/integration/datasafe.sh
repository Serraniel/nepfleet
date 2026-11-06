#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Data-safe rollback: a new version that writes into its volume and then
# fails its health check is rolled back together with that data.
# Usage: datasafe.sh <nepfleet-binary>   (needs sops and age-keygen)
set -euo pipefail
NF=$(realpath "$1"); T=$(mktemp -d); P=nepfleet-datasafe-it
fail() { echo "FAIL: $*"; exit 1; }
cleanup() {
  docker compose -p $P down -v >/dev/null 2>&1 || true
  docker volume rm $(docker volume ls -q --filter label=neptide.snapshot.of) >/dev/null 2>&1 || true
  docker network rm nepfleet_datasafe_it >/dev/null 2>&1 || true
  rm -rf "$T"
}
trap cleanup EXIT
cd "$T" && age-keygen -o key.txt 2>/dev/null && export SOPS_AGE_KEY_FILE=$T/key.txt
"$NF" init fleet >/dev/null && cd fleet
git config commit.gpgsign false; git config user.name it; git config user.email it@example.invalid
sed -i "s#    age: \[\]#    age: [\"$(age-keygen -y ../key.txt)\"]#; 0,/^spec:/s//spec:\n  ingressNetwork: nepfleet_datasafe_it/" fleet.yaml
mkdir -p stacks/$P
printf 'apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: %s}\nspec: {target: local, apply: auto}\n' $P > stacks/$P/stack.yaml
version() { # $1 version, $2 health command
  printf 'services:\n  app:\n    image: alpine:3.23\n    command: ["sh", "-c", "echo %s >> /data/log; sleep infinity"]\n    volumes: [data:/data]\n    healthcheck: {test: ["CMD", "sh", "-c", "%s"], interval: 1s, retries: 3, start_period: 2s}\nvolumes:\n  data:\n' "$1" "$2" > stacks/$P/compose.yaml
}
log() { docker run --rm -v ${P}_data:/d alpine:3.23 cat /d/log | tr '\n' ' '; }
A() { "$NF" apply --yes --no-lock --wait 30s --state "$T/state" $P; }

version v1 true; A >/dev/null
[ "$(log)" = "v1 " ] || fail "v1 data: '$(log)'"
version v2 false
out=$(A 2>&1 || true); echo "$out" | tail -5
echo "$out" | grep -q "copy their volumes" || fail "plan does not show the snapshot step"
echo "$out" | grep -qi "rolled back" || fail "v2 was not rolled back"
case "$(log)" in *v2*) fail "the broken version's data stayed: '$(log)'";; esac
[ "$(log)" = "v1 v1 " ] || fail "after rollback, the restored v1 should have started once more: '$(log)'"
[ "$(docker inspect -f '{{.State.Running}}' "$(docker compose -p $P ps -q app)")" = true ] || fail "v1 not running"
echo PASS
