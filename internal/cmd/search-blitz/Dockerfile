FROM golang:1.19.3-alpine@sha256:27a9653759f44afd08c94418307a26d2db9cf78af12933200bc2ca63c4844316 AS builder
WORKDIR /build
COPY go.sum go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o searchblitz ./internal/cmd/search-blitz

FROM sourcegraph/alpine-3.14:190630_2022-12-22_6988b6221a72@sha256:782f094e3a6397a5f57305efac7b3f0e8c992eece31c98f93c1a518cc6c876e5

COPY --from=builder /build/searchblitz /usr/local/bin

ARG COMMIT_SHA="unknown"

LABEL org.opencontainers.image.revision=${COMMIT_SHA}
LABEL org.opencontainers.image.source=https://github.com/sourcegraph/sourcegraph/internal/cmd/search-blitz

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/searchblitz"]
