# SPDX-License-Identifier: AGPL-3.0-or-later
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false \
      -ldflags "-buildid= -s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/nepfleet ./cmd/nepfleet

# NepFleet drives the Docker and Compose CLIs instead of re-implementing them,
# and git for the fleet repository, so the runtime image ships all three.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates git openssh-client docker-cli docker-cli-compose age sops \
 && adduser -D -u 65532 nepfleet
COPY --from=build /out/nepfleet /usr/local/bin/nepfleet
# Runs unprivileged by default. Managing Docker means mounting the Docker
# socket, which is root-equivalent; see docs/security.md before you do.
USER 65532:65532
WORKDIR /fleet
EXPOSE 7480
ENTRYPOINT ["nepfleet"]
# Behind Caddy, which terminates TLS. Without it, pass --tls-cert and --tls-key.
CMD ["serve", "--listen", ":7480", "--behind-proxy"]
