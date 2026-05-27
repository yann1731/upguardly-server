# Stage 1: Build
FROM golang:1.24-alpine AS builder
WORKDIR /app

RUN apk add --no-cache gcc musl-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go run github.com/steebchen/prisma-client-go generate

RUN CGO_ENABLED=1 go build -o server ./cmd/server
RUN CGO_ENABLED=1 go build -o scheduler ./cmd/scheduler

# Stage 2: Production
FROM alpine:3.21
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -S appgroup && adduser -S appuser -G appgroup

COPY --from=builder /app/server ./
COPY --from=builder /app/scheduler ./
COPY --from=builder /root/.cache/prisma /tmp/prisma

RUN chown -R appuser:appgroup /app /tmp/prisma

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

CMD ["./server"]
