# AI-CS 完整部署指南（单体镜像 + 挂载模式热更新）

> 适用版本：v2（RAG + BGE + DeepSeek + 客服分派）
> 部署方式：**单体镜像**（backend + frontend + BGE + nginx 全部在一个容器内，supervisord 管理 4 进程）
> 日常更新：**挂载模式热更新**（宿主机编译产物挂载进容器，改代码 3~5 秒生效，无需重建镜像）
> 生产环境参考：ten (122.51.255.25) / 数据库 ali (47.119.114.60 MySQL 8.0)

---

## 一、架构总览

```
                          互联网
                            │
              ┌─────────────┴─────────────┐
              │    docker run ai-cs:latest │  ← 单体镜像（一个容器）
              └─────────────┬─────────────┘
              ┌─────────────┼───────────────────┐
              │             │                   │
         supervisord 管理 4 个进程（容器内 127.0.0.1 通信）
              │             │                   │
     ┌────────▼──┐   ┌──────▼─────┐   ┌────────▼────────┐
     │ Nginx :80 │   │ Go backend │   │ BGE embedding   │
     │ (对外入口) │   │    :8080   │   │      :8088      │
     │  :3000→80 │   │  Next.js   │   │ bge-small-zh    │
     │ :18080→80 │   │ frontend   │   │ v1.5 (512维)    │
     └────────────┘   │    :3000   │   └─────────────────┘
                      └─────┬──────┘
                            │ MySQL
                      ┌─────▼──────┐
                      │  ai_cs 库  │  ← 独立数据库服务器（或本机 3306）
                      └────────────┘
```

| 容器内进程 | 端口 | supervisord startsecs | 说明 |
|-----------|------|----------------------|------|
| Go backend | 8080 | 3s | API + WebSocket，需要 MySQL |
| Next.js frontend | 3000 | 5s | 访客聊天页 + 客服工作台 |
| BGE embedding | 8088 | 30s | PyTorch 加载模型慢，startsecs 必须大 |
| Nginx | 80 | 2s | 内部反向代理 |

宿主机端口映射：`-p 3000:80`（访客/前端）、`-p 18080:80`（API/WebSocket）。两个端口映射到容器内同一个 80，由内部 nginx 按路径分发。

### 镜像

| 镜像 | 内容 | 更新频率 |
|------|------|---------|
| `registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs-base:latest` | python:3.11-slim + nginx + nodejs 20 + PyTorch + sentence-transformers + BGE 模型 (288MB) + supervisord，~5.8GB | 几乎不变（一次性构建） |
| `registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs:latest` | FROM base + Go 二进制 + Next.js 构建产物 + 配置文件，~2.8GB | 每次发版 |

**base + app 二阶段拆分**：base 只推一次，之后每次改代码只需构建/推送 app 层（增量 ~1GB），避免重复传输 5GB PyTorch 层，也绕开 ten 上 docker push 大层卡死的问题。

### 环境要求

| 组件 | 最低配置 |
|------|----------|
| 应用服务器 | 2C 4G，Docker 20+，磁盘 20G（10G 镜像 + 数据） |
| 数据库 | MySQL 8.0，可从应用服务器访问（推荐独立服务器 + SSH 隧道） |
| 模型 | BGE 模型已内置在 base 镜像，无需单独准备 |

---

## 二、数据库准备

### 2.1 建库授权（MySQL 服务器上执行）

```sql
CREATE DATABASE IF NOT EXISTS ai_cs DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'ai_cs_user'@'%' IDENTIFIED BY '<数据库密码>';
GRANT ALL PRIVILEGES ON ai_cs.* TO 'ai_cs_user'@'%';
FLUSH PRIVILEGES;
```

### 2.2 首次启动后补字段（GORM 自动建表，但以下字段需手动加）

容器首次启动会自动建表。若 `document_chunks` / `conversations` 表是旧版建的，需要补字段：

```sql
-- 向量存储（MySQL BLOB 降级模式）
ALTER TABLE document_chunks ADD COLUMN vector MEDIUMBLOB AFTER embedding_status;

-- 客服分派
ALTER TABLE conversations ADD COLUMN assigned_agent_id INT NULL AFTER agent_id;
ALTER TABLE conversations ADD COLUMN assigned_at DATETIME NULL AFTER assigned_agent_id;
ALTER TABLE conversations ADD COLUMN agent_joined_at DATETIME NULL AFTER assigned_at;

-- 轮询状态表
CREATE TABLE IF NOT EXISTS agent_dispatch_state (
    id INT PRIMARY KEY DEFAULT 1,
    next_index INT DEFAULT 0
);
INSERT IGNORE INTO agent_dispatch_state (id, next_index) VALUES (1, 0);
```

> 注意：MySQL 8.0 支持 `ADD COLUMN IF NOT EXISTS`，5.7 不支持，需先查 `SHOW COLUMNS`。

### 2.3 初始配置（新库必须手动插入，否则 AI 对话 403、RAG 不生效）

```sql
-- 1. AI 对话模型（DeepSeek；先 DESCRIBE ai_configs 确认列名）
INSERT INTO ai_configs (provider, model, api_url, api_key, is_active, is_public, model_type, created_at, updated_at)
VALUES ('deepseek', 'deepseek-v4-flash', 'https://api.deepseek.com/v1', '<DeepSeek API Key>', 1, 1, 'text', NOW(), NOW());

-- 2. BGE 嵌入配置（容器内必须用 127.0.0.1:8088；api_key 不能为空，代码检查非空才加载）
INSERT INTO embedding_configs (embedding_type, api_url, api_key, model, created_at, updated_at)
VALUES ('bge', 'http://127.0.0.1:8088', 'bge', 'bge-small-zh-v1.5', NOW(), NOW());
```

---

## 三、新服务器从零部署

### 3.1 克隆仓库（可选，若需要源码/热更新模式）

```bash
cd /root
git clone https://github.com/Yedeng626/ai-cs.git ai-cs-deploy
cd ai-cs-deploy
```

### 3.2 创建 .env

```bash
cat > /root/ai-cs-deploy/.env << 'ENVEOF'
# ============================================
# AI-CS 单体镜像环境变量
# ============================================
APP_PROFILE=docker
SERVER_HOST=0.0.0.0
SERVER_PORT=8080
GIN_MODE=release

# 数据库（容器内访问宿主机用 172.17.0.1；独立库直接填 IP）
DB_HOST=172.17.0.1
DB_PORT=3306
DB_USER=ai_cs_user
DB_PASSWORD=<数据库密码>
DB_NAME=ai_cs

# 管理员（首次启动自动创建）
ADMIN_USERNAME=admin
ADMIN_PASSWORD=<管理员密码>
ENCRYPTION_KEY=<64位hex随机串，用 openssl rand -hex 32 生成>

# Milvus 禁用（精简模式，用 MySQL BLOB 存向量）
# ⚠️ 只设 MILVUS_DISABLED=true，千万不要再设 VECTOR_STORE_DISABLED=true
MILVUS_DISABLED=true

# 钉钉通知（留空则禁用）
DINGTALK_WEBHOOK_URL=

# 会话自动关闭天数
AUTO_CLOSE_CONVERSATION_DAYS=7
ENVEOF
```

> **VECTOR_STORE_DISABLED 绝对不能设为 true**：它会彻底禁用向量化（包括 MySQL 回退），文档永远卡在 pending。正确做法是只保留 `MILVUS_DISABLED=true`。

### 3.3 拉镜像并启动

```bash
docker pull registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs:latest

docker stop ai-cs 2>/dev/null; docker rm ai-cs 2>/dev/null
docker run -d --name ai-cs --restart unless-stopped \
  -p 3000:80 -p 18080:80 \
  --env-file /root/ai-cs-deploy/.env \
  registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs:latest
```

端口可自定义（如 `-p 3006:80 -p 28080:80`），需与后续前端 API 地址一致。

### 3.4 等待 BGE 加载并验证进程

```bash
sleep 35
docker logs ai-cs --tail 10
# 应看到: success: bge entered RUNNING state
docker exec ai-cs supervisorctl -c /etc/supervisord.conf status
# backend / bge / frontend / nginx 全部 RUNNING
```

### 3.5 执行数据库初始配置

按 2.2 / 2.3 节执行 SQL（补字段 + 插入 ai_configs / embedding_configs）。

### 3.6 部署浮窗 JS（可选）

`cs-widget.js` 放到目标网站静态目录，改 iframe 地址指向本服务器：

```js
f.src = 'http://<服务器IP>:3000/chat?embed=1';
```

---

## 四、验证清单

```bash
# 前端首页
curl -s -o /dev/null -w '%{http_code}' http://<IP>:3000/            # 期望 200
# 客服工作台
curl -s -o /dev/null -w '%{http_code}' http://<IP>:3000/agent/login  # 期望 200
# 访客聊天（嵌入模式）
curl -s -o /dev/null -w '%{http_code}' "http://<IP>:3000/chat?embed=1"  # 期望 200
# API 登录
curl -s -X POST http://<IP>:18080/api/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<管理员密码>"}'
# 期望返回 JSON 含 ws_token

# 容器内 BGE 连通（必须 127.0.0.1:8088）
docker exec ai-cs curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8088/embeddings  # 期望 200
```

功能验证：
```
□ AI 对话：访客发消息 → 收到 AI 回复（无 ** Markdown 标记）
□ RAG 检索：上传文档 → 发布 → 分块向量化 → 提问命中知识库
□ 转人工：访客发"转人工" → chat_mode 切换 → 客服收到分派
□ 客服分派：客服登录工作台 → conversations.assigned_agent_id 被设置
□ 钉钉通知：转人工 / 客服 60s 未回复 → 群机器人收到消息
```

---

## 五、挂载模式热更新（日常开发推荐）

> 为什么不用每次重建镜像？Go 后端代码变更走「本地 build → push ACR → ten pull → restart」要 3~5 分钟，且大量带宽浪费在不变的 PyTorch/BGE 层上。宿主机 volume-mount 把编译产物、前端构建输出、配置挂载进容器，改代码 20 秒内生效。

### 5.1 挂载目录结构

```
/root/ai-cs-deploy/mount/
├── bin/backend              ← Go 编译产物（~34MB），挂载到 /app/backend
├── next/                    ← Next.js .next/ 构建产物，挂载到 /app/frontend/.next
├── supervisord.conf         ← 含 supervisorctl 段的配置，挂载到 /etc/supervisord.conf
└── nginx/default.conf       ← nginx 配置，挂载到 /etc/nginx/conf.d/default.conf
```

### 5.2 首次搭建（从运行中的镜像提取文件）

```bash
mkdir -p /root/ai-cs-deploy/mount/{bin,next,nginx}

docker cp ai-cs:/app/backend /root/ai-cs-deploy/mount/bin/backend
docker cp ai-cs:/app/frontend/.next/. /root/ai-cs-deploy/mount/next/
docker cp ai-cs:/etc/supervisord.conf /root/ai-cs-deploy/mount/supervisord.conf
docker cp ai-cs:/etc/nginx/conf.d/default.conf /root/ai-cs-deploy/mount/nginx/default.conf
```

> **关键**：`mount/supervisord.conf` 必须含 `[unix_http_server]`、`[rpcinterface:supervisor]`、`[supervisorctl]` 三段，否则 `supervisorctl restart backend` 报 `ini file does not include supervisorctl section`。仓库里的 `mount/supervisord.conf` 已带好，直接用。

### 5.3 以挂载模式启动容器

```bash
docker stop ai-cs && docker rm ai-cs

docker run -d --name ai-cs --restart unless-stopped \
  -p 3000:80 -p 18080:80 \
  --env-file /root/ai-cs-deploy/.env \
  -v /root/ai-cs-deploy/mount/bin/backend:/app/backend:ro \
  -v /root/ai-cs-deploy/mount/next:/app/frontend/.next:ro \
  -v /root/ai-cs-deploy/mount/supervisord.conf:/etc/supervisord.conf:ro \
  -v /root/ai-cs-deploy/mount/nginx/default.conf:/etc/nginx/conf.d/default.conf:ro \
  registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs:latest
```

### 5.4 热更新矩阵

| 改动类型 | 操作 | 生效时间 |
|----------|------|----------|
| Go 后端代码 | 宿主机 `go build -o mount/bin/backend` → `docker exec ai-cs supervisorctl -c /etc/supervisord.conf restart backend` | ~3s |
| nginx 配置 | 改 `mount/nginx/default.conf` → `docker exec ai-cs nginx -s reload` | 即时 |
| supervisord 配置 | 改 `mount/supervisord.conf` → `docker restart ai-cs` | ~35s |
| Next.js 前端 | 本地 `npm run build` → `scp .next/ ten:/root/ai-cs-deploy/mount/next/` → `supervisorctl restart frontend` | ~5s |
| BGE / serve.py | `docker restart ai-cs`（模型在镜像内，不挂载） | ~35s |

### 5.5 Go 后端热更新完整流程

```bash
# 1. 本地改代码，scp 到 ten
scp service/xxx.go ten:/root/ai-cs-deploy/backend/service/xxx.go

# 2. ten 上编译（必须用 goproxy.cn）
ssh ten "cd /root/ai-cs-deploy/backend && \
  GOPROXY=https://goproxy.cn,direct CGO_ENABLED=0 \
  go build -ldflags='-s -w' -o /root/ai-cs-deploy/mount/bin/backend ."

# 3. 重启 backend 进程（~3 秒中断）
ssh ten "docker exec ai-cs supervisorctl -c /etc/supervisord.conf restart backend"

# 4. 验证
ssh ten "docker exec ai-cs supervisorctl -c /etc/supervisord.conf status backend"
```

### 5.6 前端热更新（本地构建，别在服务器上装 Node）

ten 磁盘有限（40G），**不要在 ten 上装 Node.js**。前端改动在本地 Windows 构建：

```bash
# 本地
cd frontend
npm ci && npm run build

# 同步 .next 到 ten（删除旧产物避免残留）
ssh ten "rm -rf /root/ai-cs-deploy/mount/next/*"
scp -r .next/* ten:/root/ai-cs-deploy/mount/next/

# 重启前端进程
ssh ten "docker exec ai-cs supervisorctl -c /etc/supervisord.conf restart frontend"
```

### 5.7 注意事项

- 挂载用 `:ro`（只读），防止容器内进程意外改宿主机文件
- `supervisorctl` 必须显式带 `-c /etc/supervisord.conf`（默认路径不存在）
- Go 源码必须保留在宿主机 `/root/ai-cs-deploy/backend/`（挂载只换二进制，编译依赖源码）
- 挂载模式改动只影响挂载的文件；改完代码必须重新编译/构建再放进 mount 目录，否则不生效

---

## 六、单体镜像构建与发布（ACR）

> 推荐在**本地 Windows Docker Desktop** 构建并推送（网络稳定，不会触发 ten dockerd 大层 push 卡死）。ten 上仅作兜底。

### 6.1 同步源码

```bash
# 从 ten 拉取最新源码（本地已有一份则只需更新改动）
scp -r ten:/root/ai-cs-deploy/backend ten:/root/ai-cs-deploy/frontend \
      ten:/root/ai-cs-deploy/embedding-svc C:/Users/19314/ai-cs-mono/
```

### 6.2 构建 base（首次或 PyTorch/BGE 模型变更时，~10-15min）

```bash
cd C:/Users/19314/ai-cs-mono
docker build --no-cache -f Dockerfile.base \
  -t registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs-base:latest .
docker push registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs-base:latest
```

### 6.3 构建 app（日常发版，~3min）

```bash
cd C:/Users/19314/ai-cs-mono
docker build --no-cache -f Dockerfile.app \
  -t registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs:latest .
docker push registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs:latest
```

### 6.4 发布到服务器

```bash
ssh ten "docker pull registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs:latest && \
  docker stop ai-cs && docker rm ai-cs && \
  docker run -d --name ai-cs --restart unless-stopped \
    -p 3000:80 -p 18080:80 \
    --env-file /root/ai-cs-deploy/.env \
    registry.cn-hangzhou.aliyuncs.com/qizhi-test/ai-cs:latest"
sleep 35
ssh ten "docker logs ai-cs --tail 5"
```

> 若一直用挂载模式，发布新镜像后需重新执行 5.2 提取 + 5.3 挂载启动，否则挂载目录里的旧产物会覆盖新镜像内容。

---

## 七、常见问题排查

| 症状 | 原因 | 修复 |
|------|------|------|
| 文档导入永远 `pending` / 显示导入失败 | VECTOR_STORE_DISABLED=true 或 embedding_configs 缺失 | 检查 .env 删掉 VECTOR_STORE_DISABLED；确认 embedding_configs 有记录且 api_key 非空 |
| BGE 嵌入 404（`POST /embeddings`） | embedding_configs.api_url 是 8080 或 172.17.0.1 | 单体镜像必须 `http://127.0.0.1:8088` |
| AI 对话 403 / "暂无可用的 AI 模型" | ai_configs 缺失或 is_public=0 | 执行 2.3 插入；`UPDATE ai_configs SET is_public=1 WHERE is_active=1;` |
| BGE `gave up: entered FATAL state` | Next.js 抢占 PORT=8088 | 确认 supervisord.conf 中 frontend 有 `environment=PORT="3000"` |
| 上传大文件 413 | nginx 默认 client_max_body_size 1m | mount/nginx/default.conf 加 `client_max_body_size 50m;` + reload |
| 知识库不生效（回答不用 RAG） | 文档未发布 或 知识库 rag_enabled=false | 文档 publish + `PATCH /api/knowledge-bases/<id>/rag-enabled {"rag_enabled":true}` |
| 转人工无客服响应 | 客服未登录工作台（无 WS 连接） | 让客服打开 agent 工作台在线 |
| `ip2region` 告警 | 镜像里空占位文件 | 无害，可忽略 |
| docker push 卡在 Preparing/Waiting | ten dockerd 大层分片上传僵死 | 用新 tag 重试；根本解法是在本地构建/推送（见第六节） |

### 知识库 RAG 三步启用（常见遗漏）

```bash
# 1. 发布文档（默认 draft，RAG 只搜 published）
curl -s -X POST http://localhost:18080/api/documents/<DOC_ID>/publish \
  -H "Authorization: Bearer $TOKEN"

# 2. 删除旧 chunks + 重新分块向量化（必须带 JSON body）
curl -s -X DELETE "http://localhost:18080/api/documents/$DOC_ID/chunks" -H "Authorization: Bearer $TOKEN"
curl -s -X POST "http://localhost:18080/api/documents/$DOC_ID/chunks" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"method":"char_count","chunk_size":500,"chunk_overlap":50}'

# 3. 知识库开启 RAG
curl -s -X PATCH "http://localhost:18080/api/knowledge-bases/$KB_ID/rag-enabled" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"rag_enabled":true}'
```

---

## 八、安全加固建议

| 项 | 建议 |
|------|------|
| HTTPS | Nginx 前套 Let's Encrypt 证书（ali 的 yedeng.top 已做） |
| 数据库 | 密码改强、限定来源 IP、不用 root 连应用 |
| SSH 隧道 | 用 autossh + systemd 保活 |
| 镜像 tag | 发版打版本号 tag（`:v1.1`），别只覆盖 `latest`，便于回滚 |
| .env 权限 | `chmod 600 .env`，不入 git 库 |

---

## 九、当前生产环境速查（ten）

| 项 | 值 |
|----|-----|
| 服务器 | 122.51.255.25（CentOS 7.9, 4C/3.6G） |
| 项目目录 | /root/ai-cs-deploy/（git 仓库, origin=git@github.com:Yedeng626/ai-cs.git） |
| 挂载目录 | /root/ai-cs-deploy/mount/ |
| 容器 | ai-cs（单体镜像, supervisord 4 进程） |
| 端口 | 3000:80（前端/访客）、18080:80（API/WebSocket） |
| MySQL | ali 47.119.114.60:3306，SSH 隧道 172.17.0.1:3306，库 ai_cs |
| 管理员 | admin / 见 .env ADMIN_PASSWORD |
| AI 模型 | deepseek-v4-flash（api.deepseek.com/v1） |
| BGE | 容器内 127.0.0.1:8088，bge-small-zh-v1.5（512维） |
| 浮窗 JS | ali /usr/share/nginx/html/cs-widget.js（iframe → ai-cs :3000/chat?embed=1） |
