#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# lock.yaml pins digests: a moved tag does not change a deployment, an
# explicit --update-lock does, and a git rollback restores the old digest.
# Usage: lock.sh <nepfleet-binary>   (needs sops and age-keygen)
set -euo pipefail
NF=$(realpath "$1"); T=$(mktemp -d); R=127.0.0.1:15001; IMG=$R/nepfleet-lock-it:1; P=nepfleet-lock-it
fail() { echo "FAIL: $*"; exit 1; }
cleanup() { docker compose -p $P down -v >/dev/null 2>&1 || true; docker rm -f nepfleet-lock-registry >/dev/null 2>&1 || true; docker rmi -f $IMG >/dev/null 2>&1 || true; }
trap cleanup EXIT
docker run -d --name nepfleet-lock-registry -p $R:5000 registry:3.1 >/dev/null; sleep 2
push() { mkdir -p "$T/img" && printf 'FROM alpine:3.23\nRUN echo %s > /version\nCMD ["sleep","infinity"]\n' "$1" > "$T/img/Dockerfile"; docker build -q -t $IMG "$T/img" >/dev/null 2>&1; docker push -q $IMG >/dev/null; }
running() { docker exec "$(docker compose -p $P ps -q app)" cat /version; }

cd "$T" && age-keygen -o key.txt 2>/dev/null && export SOPS_AGE_KEY_FILE=$T/key.txt
"$NF" init fleet >/dev/null && cd fleet
git config commit.gpgsign false; git config user.name it; git config user.email it@example.invalid
sed -i "s#    age: \[\]#    age: [\"$(age-keygen -y ../key.txt)\"]#" fleet.yaml
mkdir -p stacks/$P
printf 'apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: %s}\nspec: {target: local, apply: auto}\n' $P > stacks/$P/stack.yaml
printf 'services:\n  app:\n    image: %s\n' $IMG > stacks/$P/compose.yaml
A() { "$NF" apply --yes --state "$T/state" "$@" $P; }

push v1; docker rmi $IMG >/dev/null   # force resolution through the registry
A | grep -q 'digests pinned' || fail "lock.yaml not written"
d1=$(grep -o 'sha256:[0-9a-f]*' stacks/$P/lock.yaml)
git add -A && git commit -qm "v1"
[ "$(running)" = v1 ] || fail "v1 not running"

push v2   # same tag, new digest
A >/dev/null
[ "$(running)" = v1 ] || fail "moved tag changed a pinned deployment"

A --update-lock | grep -q 'digests pinned' || fail "--update-lock did not move the pin"
[ "$(running)" = v2 ] || fail "v2 not running after --update-lock"
[ "$(grep -o 'sha256:[0-9a-f]*' stacks/$P/lock.yaml)" != "$d1" ] || fail "lock unchanged"
git commit -qam "v2"

"$NF" rollback --yes --state "$T/state" $P >/dev/null
[ "$(running)" = v1 ] || fail "rollback did not restore the pinned v1 digest (tag points to v2)"
echo PASS
