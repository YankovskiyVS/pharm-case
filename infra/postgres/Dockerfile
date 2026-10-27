# Rebuild the upstream privilege-drop helper with the patched Go standard library.
# PostgreSQL and its official entrypoint remain the upstream distribution.
FROM golang:1.27.3-alpine AS gosu
RUN CGO_ENABLED=0 go install -trimpath -ldflags="-s -w" github.com/tianon/gosu@1.19

FROM postgres:16.15-alpine
COPY --from=gosu /go/bin/gosu /usr/local/bin/gosu
RUN gosu --version && test "$(gosu postgres id -un)" = postgres
