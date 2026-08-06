# ============================================
# AI-CS App — 应用层（频繁变更）
# 基于 ai-cs-base，只加 Go 后端 + Next.js + 配置
# 构建约 3 分钟，推送约 1-2 分钟
# ============================================

# --- Stage 1: Build Go ---
FROM golang:1.24-alpine AS go-builder
WORKDIR /app
RUN apk add --no-cache git ca-certificates tzdata
COPY backend/go.mod backend/go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download
COPY backend/ ./
RUN mkdir -p /app/data && touch /app/data/ip2region_v4.xdb /app/data/ip2region_v6.xdb
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo \
    -ldflags '-extldflags "-static" -s -w' -o backend main.go

# --- Stage 2: Build Next.js ---
FROM node:20-alpine AS node-builder
WORKDIR /app
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# --- Stage 3: Assemble ---
FROM registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs-base:latest

COPY --from=go-builder /app/backend /app/backend
COPY --from=go-builder /app/data/ip2region_v4.xdb /app/data/ip2region_v4.xdb
COPY --from=go-builder /app/data/ip2region_v6.xdb /app/data/ip2region_v6.xdb
RUN chmod +x /app/backend

COPY --from=node-builder /app/package*.json /app/frontend/
COPY --from=node-builder /app/next.config.mjs /app/frontend/
COPY --from=node-builder /app/public /app/frontend/public
COPY --from=node-builder /app/.next /app/frontend/.next
RUN cd /app/frontend && npm ci --only=production && npm cache clean --force

COPY nginx-internal.conf /etc/nginx/conf.d/default.conf
RUN rm -f /etc/nginx/sites-enabled/default

COPY supervisord.conf /etc/supervisord.conf

EXPOSE 80
CMD ["supervisord", "-c", "/etc/supervisord.conf"]
