FROM golang:1.22-alpine AS builder
WORKDIR /app
# The official Go images set GOTOOLCHAIN=local, which blocks Go from fetching
# a newer toolchain even if a dependency's go.mod requires one. `auto` lets
# `go mod tidy`/`go build` download the exact version needed at build time.
ENV GOTOOLCHAIN=auto
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o /server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /server /app/server
COPY migrations ./migrations
EXPOSE 8080
ENTRYPOINT ["/app/server"]
