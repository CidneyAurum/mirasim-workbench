# syntax=docker/dockerfile:1

# ---- 构建阶段 ----
FROM golang:1.27-alpine AS builder

WORKDIR /src
# 先拷依赖清单，利用层缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 纯 Go（modernc.org/sqlite 无 cgo），可静态编译
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mirasim2api ./cmd/server

# ---- 运行阶段 ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app
WORKDIR /app
COPY --from=builder /out/mirasim2api /app/mirasim2api

# 数据目录（SQLite + master.key），以 volume 持久化
ENV DATA_DIR=/app/data \
    HOST=0.0.0.0 \
    PORT=8787
RUN mkdir -p /app/data && chown -R app:app /app
VOLUME ["/app/data"]

USER app
EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -q -O /dev/null http://127.0.0.1:8787/health || exit 1

ENTRYPOINT ["/app/mirasim2api"]
