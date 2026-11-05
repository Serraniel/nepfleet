#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Importing a stack from a real Portainer CE: the compose file and env come
# from its API, secrets are encrypted, relative bind mounts keep pointing at
# the data in Portainer's volume, and the first apply recreates nothing.
# Usage: portainer.sh <nepfleet-binary>   (needs docker, sops, age-keygen, curl, jq)
set -euo pipefail
NF=$(realpath "$1"); T=$(mktemp -d); C=nf-portainer-it; V=nf-portainer-it-data; P=pt-web; API=http://127.0.0.1:19000
fail() { echo "FAIL: $*"; exit 1; }
cleanup() {
  docker compose -p $P down >/dev/null 2>&1 || true
  docker rm -f $C >/dev/null 2>&1 || true; docker volume rm $V >/dev/null 2>&1 || true
  rm -rf "$T"
}
trap cleanup EXIT
docker run -d --name $C -p 127.0.0.1:19000:9000 -v /var/run/docker.sock:/var/run/docker.sock -v $V:/data \
  portainer/portainer-ce:2.33.3 >/dev/null
for _ in $(seq 60); do curl -fs $API/api/system/status >/dev/null && break; sleep 1; done
pw=$(head -c 18 /dev/urandom | base64 | tr -d '/+=')   # throwaway admin of this test instance
curl -fsS -X POST $API/api/users/admin/init -H 'Content-Type: application/json' -d "{\"Username\":\"admin\",\"Password\":\"$pw\"}" >/dev/null
jwt=$(curl -fsS -X POST $API/api/auth -H 'Content-Type: application/json' -d "{\"Username\":\"admin\",\"Password\":\"$pw\"}" | jq -r .jwt)
curl -fsS -X POST $API/api/endpoints -H "Authorization: Bearer $jwt" -F Name=local -F EndpointCreationType=1 >/dev/null
export PORTAINER_TOKEN=$(curl -fsS -X POST $API/api/users/1/tokens -H "Authorization: Bearer $jwt" -H 'Content-Type: application/json' \
  -d "{\"password\":\"$pw\",\"description\":\"nepfleet-it\"}" | jq -r .rawAPIKey)
compose=$(printf 'services:\n  web:\n    image: nginx:1.29-alpine\n    environment: {GREETING: "${GREETING}", DB_PASSWORD: "${DB_PASSWORD}"}\n    volumes: ["./html:/usr/share/nginx/html:ro"]\n')
jq -n --arg name $P --arg file "$compose" '{name: $name, stackFileContent: $file, env: [{name: "GREETING", value: "hi"}, {name: "DB_PASSWORD", value: "s3cr3t"}]}' |
  curl -fsS -X POST "$API/api/stacks/create/standalone/string?endpointId=1" -H "Authorization: Bearer $jwt" -H 'Content-Type: application/json' -d @- >/dev/null
data=$(docker volume inspect -f '{{.Mountpoint}}' $V)
docker run --rm -v $V:/data alpine:3.23 sh -c 'mkdir -p /data/compose/1/html && echo "portainer data" > /data/compose/1/html/index.html'
before=$(docker compose -p $P ps -q web)
[ -n "$before" ] || fail "Portainer did not start the stack"

cd "$T" && age-keygen -o key.txt 2>/dev/null && export SOPS_AGE_KEY_FILE=$T/key.txt
"$NF" init fleet >/dev/null && cd fleet
sed -i "s#    age: \[\]#    age: [\"$(age-keygen -y ../key.txt)\"]#" fleet.yaml
"$NF" import portainer --url $API | grep -q "$P .*compose" || fail "stack not listed"
"$NF" import portainer --url $API --state "$T/state" --target local --portainer-data "$data" $P | tee ../import.log
grep -q 'secrets (encrypted):.*DB_PASSWORD' ../import.log || fail "DB_PASSWORD not encrypted"
grep -rq s3cr3t stacks/$P/stack.yaml stacks/$P/compose.yaml && fail "secret in plaintext"
grep -q "$data/compose/1/html" stacks/$P/compose.yaml || fail "relative bind mount does not point into Portainer's volume"
"$NF" apply --yes --state "$T/state" $P | grep -q 'up to date' || fail "apply after import changed something"
[ "$(docker compose -p $P ps -q web)" = "$before" ] || fail "container was recreated"
echo PASS
