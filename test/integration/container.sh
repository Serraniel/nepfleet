#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# The container install from docs/install.md: only Docker on the host. Key
# generation, init and a health-gated apply all run in the NepFleet image,
# a relative bind mount resolves on the host, and the API starts.
# Usage: container.sh <image>   (needs docker and curl, nothing else)
set -euo pipefail
IMG=$1; T=$(mktemp -d); P=nepfleet-container-it; S=nepfleet-container-app; NET=nepfleet_container_it
fail() { echo "FAIL: $*"; exit 1; }
cleanup() {
  [ -f "$T/compose.yaml" ] && docker compose -f "$T/compose.yaml" -p $P down >/dev/null 2>&1 || true
  docker compose -p $S down >/dev/null 2>&1 || true
  docker network rm $NET >/dev/null 2>&1 || true
  # The test app wrote its data as root.
  docker run --rm -v "$T:$T" alpine:3.24 rm -rf "$T/state" >/dev/null 2>&1 || true
  rm -rf "$T"
}
trap cleanup EXIT

# compose.yaml as shipped, with this test's directory and image.
sed -e "s#/srv/nepfleet#$T#g" -e "s#image: .*#image: $IMG#" -e "s#127.0.0.1:7480:7480#127.0.0.1:17480:7480#" \
  "$(dirname "$0")/../../compose.yaml" > "$T/compose.yaml"
cd "$T" && mkdir fleet state
cat > .env <<EOF
NEPFLEET_UID=$(id -u)
NEPFLEET_GID=$(id -g)
DOCKER_GID=$(getent group docker | cut -d: -f3)
NEPFLEET_HOST=it-host
EOF
# Compose progress goes to stderr; keep the rest of stderr for CI logs.
nf() { docker compose -p $P run --rm -T "$@" 2> >(grep -v -e "Container $P" -e "Network $P" >&2); }

nf --entrypoint age-keygen nepfleet -o "$T/age.key" >/dev/null
pub=$(nf --entrypoint age-keygen nepfleet -y "$T/age.key")
nf nepfleet init . >/dev/null
[ "$(stat -c %u fleet/fleet.yaml)" = "$(id -u)" ] || fail "fleet repository not owned by the host user"
sed -i "s#    age: \[\]#    age: [\"$pub\"]#" fleet/fleet.yaml
# Own ingress network, so the test never touches a real install's.
printf '  ingressNetwork: %s\n' $NET >> fleet/fleet.yaml

mkdir -p fleet/stacks/$S
printf 'apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: %s}\nspec: {target: it-host, apply: auto}\n' $S > fleet/stacks/$S/stack.yaml
cat > fleet/stacks/$S/compose.yaml <<'EOF'
services:
  app:
    image: alpine:3.24
    command: ["sh", "-c", "date > /data/started && sleep infinity"]
    volumes: ["./data:/data"]
    healthcheck: {test: ["CMD", "test", "-f", "/data/started"], interval: 1s, retries: 20}
EOF

nf nepfleet doctor > "$T/doctor.log" || { cat "$T/doctor.log"; fail "doctor failed in the container"; }
nf nepfleet apply --yes $S | tee "$T/apply.log" >/dev/null || { cat "$T/apply.log"; fail "apply from the container failed"; }
find "$T/state" -name started | grep -q . || fail "relative bind mount did not resolve under the host state directory"

docker compose -p $P up -d >/dev/null 2>&1
for _ in $(seq 20); do curl -fs -o /dev/null http://127.0.0.1:17480/healthz && break; sleep 1; done
curl -fs -o /dev/null http://127.0.0.1:17480/healthz || fail "API did not start"
# The health check neptide gates self-updates on must pass.
for _ in $(seq 30); do [ "$(docker inspect -f '{{.State.Health.Status}}' "$(docker compose -p $P ps -q nepfleet)")" = healthy ] && break; sleep 2; done
[ "$(docker inspect -f '{{.State.Health.Status}}' "$(docker compose -p $P ps -q nepfleet)")" = healthy ] || fail "container health check does not pass"
[ "$(docker inspect -f '{{index .Config.Labels "neptide.enable"}}' "$(docker compose -p $P ps -q nepfleet)")" = true ] || fail "not opted in to neptide updates"
echo PASS
