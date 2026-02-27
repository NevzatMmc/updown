# ── Build stage ─────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build both binaries
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/server     ./cmd/server/...
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/backoffice ./cmd/backoffice/...

# ── Runtime stage ────────────────────────────────────────────────
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/server     ./server
COPY --from=builder /out/backoffice ./backoffice
COPY migrations/    ./migrations/

# Default: run the main API server
# Override CMD in docker-compose for backoffice
EXPOSE 8080
CMD ["./server"]
