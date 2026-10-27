FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod main.go main_test.go ./
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -o /backup-crypt .
FROM alpine:3.22
COPY --from=build /backup-crypt /backup-crypt
USER 10001:10001
# Same UID as the API: read its private 0600 objects without DAC capabilities.
CMD ["/backup-crypt", "encrypt"]
