# Keep the upstream Caddy release/modules; replace the vulnerable Go toolchain
# and x/net dependency until the official runtime image incorporates these fixes.
FROM golang:1.27.3-alpine AS build
WORKDIR /src
RUN go mod download github.com/caddyserver/caddy/v2@v2.11.7 \
    && cp -a /go/pkg/mod/github.com/caddyserver/caddy/v2@v2.11.7/. /src/ \
    && chmod -R u+w /src \
    && go get golang.org/x/net@v0.60.0 \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/caddyserver/caddy/v2.CustomVersion=v2.11.7" -o /caddy ./cmd/caddy

FROM caddy:2.11.7-alpine
RUN apk upgrade --no-cache
COPY --from=build /caddy /usr/bin/caddy
RUN setcap cap_net_bind_service=+ep /usr/bin/caddy && caddy version
