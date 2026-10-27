FROM golang:1.27.3-alpine AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
FROM source AS test
RUN apk add --no-cache gcc musl-dev
CMD ["go", "test", "-race", "-cover", "./..."]
FROM source AS build
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server
FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates && addgroup -g 10001 app && adduser -D -u 10001 -G app app
RUN mkdir /audio && chown app:app /audio
COPY --from=build /server /server
USER app
EXPOSE 8000
HEALTHCHECK --interval=5s --timeout=4s --start-period=10s CMD ["/server", "healthcheck"]
ENTRYPOINT ["/server"]
