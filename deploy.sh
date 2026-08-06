#!/bin/bash
# ============================================================
# AI-CS 一键部署脚本（单体镜像版）
#
# 适用: 单体镜像架构（backend+frontend+BGE+nginx 单容器, supervisord 4进程）
# 用法: chmod +x deploy.sh && ./deploy.sh
#
# 行为:
#   - 自动检测挂载目录 mount/，存在则启动为挂载模式（热更新）
#   - 数据库迁移: 本机有 mysql client 且能连上 DB_HOST 时自动执行
#   - 无 mysql client 时给出远程迁移提示（可 --skip-migrate 跳过）
#
# 选项:
#   ./deploy.sh --skip-migrate   跳过数据库迁移
#   ./deploy.sh --no-pull        不重新拉镜像（本地已有）
#   ./deploy.sh --keep-data      不删除旧容器（直接替换）
# ============================================================
set -e

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}[✓]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
fail() { echo -e "${RED}[✗]${NC} $1"; exit 1; }

SKIP_MIGRATE=false
NO_PULL=false
for arg in "$@"; do
    case "$arg" in
        --skip-migrate) SKIP_MIGRATE=true ;;
        --no-pull)      NO_PULL=true ;;
        --keep-data)    KEEP_DATA=true ;;
    esac
done

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$PROJECT_DIR"

IMAGE="registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs:latest"
CONTAINER="ai-cs"
WEB_PORT="${WEB_PORT:-3000}"      # 前端/访客
API_PORT="${API_PORT:-18080}"     # API/WebSocket

echo "============================================"
echo "  AI-CS 智能客服 — 单体镜像一键部署"
echo "  项目目录: $PROJECT_DIR"
echo "  镜像:     $IMAGE"
echo "  端口:     ${WEB_PORT}:80 (前端)  ${API_PORT}:80 (API)"
echo "============================================"
echo ""

# ── 0. 环境检查 ────────────────────────────────────────
echo ">>> 检查环境..."
which docker >/dev/null 2>&1 || fail "Docker 未安装"
[ -f .env ] || fail ".env 文件不存在，请先参照 DEPLOY.md 第三节创建"
grep -q '^DB_PASSWORD=' .env || warn ".env 中缺少 DB_PASSWORD"
grep -q '^ADMIN_PASSWORD=' .env || warn ".env 中缺少 ADMIN_PASSWORD"
ok "环境检查通过"

# ── 1. 数据库迁移 ──────────────────────────────────────
if [ "$SKIP_MIGRATE" = "false" ]; then
    echo ""
    echo ">>> 数据库迁移..."
    if command -v mysql >/dev/null 2>&1; then
        DB_HOST=$(grep '^DB_HOST=' .env | cut -d= -f2)
        DB_PORT=$(grep '^DB_PORT=' .env | cut -d= -f2)
        DB_USER=$(grep '^DB_USER=' .env | cut -d= -f2)
        DB_PASSWORD=$(grep '^DB_PASSWORD=' .env | cut -d= -f2)
        DB_NAME=$(grep '^DB_NAME=' .env | cut -d= -f2)
        DB_HOST="${DB_HOST:-172.17.0.1}"; DB_PORT="${DB_PORT:-3306}"
        DB_NAME="${DB_NAME:-ai_cs}"

        if mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASSWORD" "$DB_NAME" -e "SELECT 1" &>/dev/null; then
            ok "数据库连接正常 ($DB_USER@$DB_HOST:$DB_PORT/$DB_NAME)"
            mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASSWORD" "$DB_NAME" 2>/dev/null <<'SQL'
ALTER TABLE document_chunks ADD COLUMN IF NOT EXISTS vector MEDIUMBLOB AFTER embedding_status;
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS assigned_agent_id INT NULL AFTER agent_id;
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS assigned_at DATETIME NULL AFTER assigned_agent_id;
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS agent_joined_at DATETIME NULL AFTER assigned_at;
CREATE TABLE IF NOT EXISTS agent_dispatch_state (
    id INT PRIMARY KEY DEFAULT 1,
    next_index INT DEFAULT 0
);
INSERT IGNORE INTO agent_dispatch_state (id, next_index) VALUES (1, 0);
UPDATE ai_configs SET is_public=1 WHERE is_active=1 AND is_public=0;
SQL
            ok "数据库迁移完成"
        else
            warn "无法连接数据库 ($DB_USER@$DB_HOST:$DB_PORT)，跳过迁移"
            warn "请检查 SSH 隧道: ssh -f -N -L 3306:<DB_IP>:3306 root@<DB_IP> -i <key>"
            warn "或参照 DEPLOY.md 第二节手动执行 SQL"
        fi
    else
        warn "本机无 mysql client，跳过迁移"
        warn "可在有 mysql 的机器执行 DEPLOY.md 第二节的 SQL，或用 --skip-migrate 跳过"
    fi
else
    echo ">>> 数据库迁移: 已跳过 (--skip-migrate)"
fi

# ── 2. 拉取镜像 ────────────────────────────────────────
echo ""
echo ">>> 拉取镜像..."
if [ "$NO_PULL" = "true" ]; then
    ok "跳过拉取 (--no-pull)"
else
    docker pull "$IMAGE"
    ok "镜像已就绪"
fi

# ── 3. 停止旧容器 ──────────────────────────────────────
echo ""
echo ">>> 停止旧容器..."
if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
    docker stop "$CONTAINER" >/dev/null 2>&1 || true
    if [ "$KEEP_DATA" != "true" ]; then
        docker rm "$CONTAINER" >/dev/null 2>&1 || true
        ok "旧容器 $CONTAINER 已删除"
    else
        ok "旧容器已停止 (--keep-data, 未删除)"
    fi
else
    ok "无旧容器"
fi

# ── 4. 启动容器 ────────────────────────────────────────
echo ""
echo ">>> 启动容器..."
if [ -d mount ] && [ -n "$(ls -A mount 2>/dev/null)" ]; then
    warn "检测到 mount/ 目录，以【挂载模式】启动（热更新）"
    MOUNT_ARGS="
        -v $PROJECT_DIR/mount/bin/backend:/app/backend:ro \
        -v $PROJECT_DIR/mount/next:/app/frontend/.next:ro \
        -v $PROJECT_DIR/mount/supervisord.conf:/etc/supervisord.conf:ro \
        -v $PROJECT_DIR/mount/nginx/default.conf:/etc/nginx/conf.d/default.conf:ro"
else
    warn "未检测到 mount/ 目录，以【纯镜像模式】启动"
    MOUNT_ARGS=""
fi

docker run -d --name "$CONTAINER" --restart unless-stopped \
    -p "$WEB_PORT:80" -p "$API_PORT:80" \
    --env-file "$PROJECT_DIR/.env" \
    $MOUNT_ARGS \
    "$IMAGE"
ok "容器已启动"

# ── 5. 等待 BGE 加载 ───────────────────────────────────
echo ""
echo ">>> 等待 BGE 模型加载 (约 35s)..."
sleep 35
docker exec "$CONTAINER" supervisorctl -c /etc/supervisord.conf status

# ── 6. 验证 ────────────────────────────────────────────
echo ""
echo ">>> 验证服务..."
check_http() {
    local name="$1" url="$2"
    local code=$(curl -s -o /dev/null -w '%{http_code}' --connect-timeout 5 "$url" 2>/dev/null)
    if [ "$code" = "200" ]; then
        ok "$name ($url → $code)"
    else
        warn "$name ($url → $code, 期望 200)"
    fi
}
check_http "AI-CS 前端" "http://127.0.0.1:$WEB_PORT/"
check_http "客服工作台" "http://127.0.0.1:$WEB_PORT/agent/login"
check_http "访客聊天(嵌入)" "http://127.0.0.1:$WEB_PORT/chat?embed=1"
check_http "API health" "http://127.0.0.1:$API_PORT/health"

echo ""
echo "============================================"
echo "  部署完成"
echo "============================================"
echo ""
echo "  前端:     http://<服务器IP>:$WEB_PORT/"
echo "  客服后台: http://<服务器IP>:$WEB_PORT/agent/login"
echo "  API:      http://<服务器IP>:$API_PORT/health"
echo "  默认账号: admin / 见 .env 中 ADMIN_PASSWORD"
echo ""
echo "  常用命令:"
echo "    进程状态:   docker exec ai-cs supervisorctl -c /etc/supervisord.conf status"
echo "    重启后端:   docker exec ai-cs supervisorctl -c /etc/supervisord.conf restart backend"
echo "    查看日志:   docker logs ai-cs --tail 50"
echo ""
