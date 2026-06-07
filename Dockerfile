# Stage 0: Dev (local development — no apk, no separate runtime stage)
FROM golang:1.25 AS dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
# air provides live-reload: it rebuilds and restarts the binary on file changes.
# Installed here (build runs with network: host) so the container never needs
# network access at startup to fetch it.
RUN go install github.com/air-verse/air@latest
COPY . .
RUN go run github.com/steebchen/prisma-client-go generate
EXPOSE 8080
CMD ["go", "run", "./cmd/server"]

# Stage 1: Build
FROM golang:1.25-alpine AS builder
WORKDIR /app

RUN apk add --no-cache gcc musl-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go run github.com/steebchen/prisma-client-go generate

RUN CGO_ENABLED=1 go build -o server ./cmd/server
RUN CGO_ENABLED=1 go build -o scheduler ./cmd/scheduler
RUN go build -o prisma-cli github.com/steebchen/prisma-client-go

# Stage 2: Production
FROM alpine:3.21
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -S appgroup && adduser -S appuser -G appgroup

COPY --from=builder /app/server ./
COPY --from=builder /app/scheduler ./
COPY --from=builder /app/prisma-cli ./
COPY --from=builder /root/.cache/prisma /tmp/prisma
COPY prisma/schema.prisma ./prisma/schema.prisma
COPY prisma/migrations ./prisma/migrations
COPY entrypoint.sh ./
RUN chmod +x entrypoint.sh

# Create the scheduler's data dir owned by appuser. A fresh named volume mounted
# here inherits this ownership, so the non-root user can create scheduler.db.
RUN mkdir -p /app/data && chown -R appuser:appgroup /app /tmp/prisma

USER appuser

EXPOSE 8080

# Server configuration
ENV PORT=8080

# Scheduler configuration
ENV SCHEDULER_INSTANCE_ID=""
ENV SCHEDULER_PARTITION_COUNT=64
ENV SCHEDULER_SYNC_INTERVAL=10s
ENV SCHEDULER_LEASE_TTL=10s
ENV SQLITE_PATH=/app/data/scheduler.db
ENV ETCD_ENDPOINTS=localhost:2379

# Point prisma-client-go's cache (os.UserCacheDir -> $XDG_CACHE_HOME/prisma) at
# the binaries bundled at build time under /tmp/prisma. Without this it resolves
# to $HOME/.cache/prisma, finds nothing, and tries to download the engine at
# startup — which crash-loops the container when prisma.sh is unreachable.
ENV XDG_CACHE_HOME=/tmp

ENTRYPOINT ["./entrypoint.sh"]
CMD ["./server"]
