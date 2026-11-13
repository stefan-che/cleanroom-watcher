FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o scanner ./cmd/scanner

# ---

FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/scanner .
COPY --from=builder /app/migrations ./migrations
# config.json is NOT baked into the image (it holds secrets): docker-compose.yml mounts it
# read-only at /app/config.json. Start from config.docker.json.example.

EXPOSE 8080

CMD ["./scanner"]
