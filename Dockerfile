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
EXPOSE 8080
CMD ["go", "run", "./cmd/server"]

# Stage 1: Build
FROM golang:1.25-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o server ./cmd/server
RUN CGO_ENABLED=0 go build -o scheduler ./cmd/scheduler
# migrate applies the embedded bun migrations (replaces prisma migrate deploy).
RUN CGO_ENABLED=0 go build -o migrate ./cmd/migrate

# Stage 2: Production
FROM alpine:3.21
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -S appgroup && adduser -S appuser -G appgroup

COPY --from=builder /app/server ./
COPY --from=builder /app/scheduler ./
COPY --from=builder /app/migrate ./
COPY entrypoint.sh ./
RUN chmod +x entrypoint.sh

RUN chown -R appuser:appgroup /app

USER appuser

EXPOSE 8080

# Server configuration. Scheduler configuration (SCHEDULER_*, ETCD_ENDPOINT)
# comes from the environment — see internal/config/config.go for names and
# defaults.
ENV PORT=8080

ENTRYPOINT ["./entrypoint.sh"]
CMD ["./server"]
