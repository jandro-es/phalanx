# Phalanx — Go Production Dockerfile
# Produces a ~20MB static binary in a distroless image.

FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/phalanx-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/phalanx ./cmd/cli

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /bin/phalanx-server /phalanx-server
COPY --from=builder /bin/phalanx /phalanx
COPY --from=builder /src/migrations /migrations
COPY --from=builder /src/skills /skills
EXPOSE 3100
USER nonroot:nonroot
ENTRYPOINT ["/phalanx-server"]
