# Estágio de construção
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache \
    make \
    gcc 

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o collector .

FROM alpine:3.18

RUN apk add --no-cache \
    curl \
    tzdata

RUN addgroup -S appgroup && adduser -S appuser -G appgroup
RUN mkdir -p /app/logs && \
    touch /app/logs/collector.log && \
    chown -R appuser:appgroup /app/logs

USER appuser

WORKDIR /app

COPY --from=builder --chown=appuser:appgroup /app/collector .

ENTRYPOINT ["./collector"]