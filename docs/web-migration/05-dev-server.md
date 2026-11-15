# 开发服务器环境

> 服务器：`<SERVER_IP>:<SSH_PORT>`（SSH 别名 `srv253`）
> 用途：构建、运行、调试全部在服务器；本机只编辑代码
> 原因：本机磁盘只剩 7.6G，不够装 node_modules + Go 模块

---

## 一、服务器现状

| 项 | 值 |
|---|---|
| 系统 | Ubuntu 24.04.1 LTS |
| CPU | 8 核 AMD EPYC 7K62 |
| 内存 | 7.8 G（已用 ~4 G）+ **新增 8 G swap** |
| 磁盘 | 117 G，**可用 63 G**（清理 Docker 缓存后从 55G 涨到 63G） |
| Docker | 29.8.2 / Compose v5.6.0 |
| 已有服务 | dpxwebsite、sub2api、agent-room、1Panel 等 13 个容器（**不要动**） |

**新增的 swap**：`/swapfile` 8G，`vm.swappiness=10`，已写入 `/etc/fstab` 持久化。
原因：构建 Go/C#/前端时容易 OOM，服务器原本没有 swap。

---

## 二、目录与端口约定

```
服务器 /opt/yibiao-dev/
├── Dockerfile.dev          开发镜像定义
├── docker-compose.dev.yml  编排（dev 容器 + postgres）
└── workspace/              代码（rsync 同步自本机）
```

| 服务 | 容器端口 | 服务器端口 | 公网访问 |
|---|---|---|---|
| 前端 Vite | 5173 | **5180** | http://<SERVER_IP>:5180/ |
| Go API | 8080 | **8090** | http://<SERVER_IP>:8090/health |
| PostgreSQL | 5432 | **5433** | 仅 `127.0.0.1`，不对外 |
| Chromium CDP | 9222 | **9222** | 已放行（调试用，用完可关） |

防火墙（ufw）已放行 `5180/tcp`、`8090/tcp`。

**端口选择理由**：全部避开了服务器已有服务占用的端口（53/80/443/3000/3213/8080/9002/9003/<SSH_PORT>/18800 等）。

---

## 三、容器内工具链

| 工具 | 版本 |
|---|---|
| Go | 1.27.1 |
| Node | 22.22.2 |
| npm / pnpm | 10.9.7 / 10.34.9 |
| .NET SDK | 10.0.404 |
| LibreOffice | 24.2.7 |
| 中文字体 | 85 个（Noto CJK + 文泉驿） |
| 其他 | git / jq / ripgrep / fd / postgresql-client / tesseract |

### 字体映射（关键）

容器里没有宋体/黑体，已配置 `/etc/fonts/conf.d/99-yibiao.conf` 做别名映射：

| 原名 | 映射到 | 验证 |
|---|---|---|
| 宋体 / SimSun | Noto Serif CJK SC | ✅ `fc-match 宋体` 已确认 |
| 黑体 / SimHei | Noto Sans CJK SC | ✅ `fc-match 黑体` 已确认 |
| 微软雅黑 / Microsoft YaHei | Noto Sans CJK SC | ✅ |

**为什么这很重要**：docx 里写的是「宋体」，服务端做格式自检/页码测量/PDF 转换时如果找不到字体，会回退到别的字体，导致**行高和页数算错**。这是迁移方案里标记的隐藏坑，现已提前解决。

---

## 四、日常开发流程

### 1. 改代码（本机）

正常在本机 IDE 编辑 `~/Documents/Code/go/OpenBidKit_Yibiao/`。

### 2. 同步到服务器

```bash
./scripts/dev/sync-to-server.sh          # 增量
./scripts/dev/sync-to-server.sh --full   # 全量（checksum 对比）
./scripts/dev/sync-to-server.sh --dry    # 只看会传什么
```

**不会同步**：`node_modules`、`dist`、`release`、`.git`、`.workbuddy`、构建产物。
**不会删除**服务器上的 `node_modules`，所以依赖装一次就够。

### 3. 启动服务

```bash
ssh srv253
cd /opt/yibiao-dev
./dev-up.sh          # 或 docker compose -f docker-compose.dev.yml up -d
```

### 4. 浏览器访问

```
http://<SERVER_IP>:5180/
```

改代码后 vite HMR 自动刷新。

### 5. 看日志

```bash
ssh srv253 "docker exec yibiao-dev-workspace tail -f /tmp/vite.log"
# 或
ssh srv253 "cd /opt/yibiao-dev && docker compose -f docker-compose.dev.yml logs -f dev"
```

---

## 五、常用命令

```bash
# 进容器
ssh -t srv253 "docker exec -it yibiao-dev-workspace bash"

# 手动启停 vite
ssh srv253 "docker exec -d yibiao-dev-workspace bash -c 'cd /workspace/client && PUBLIC_HOST=<SERVER_IP> PUBLIC_PORT=5180 npx vite --config vite.web.config.ts > /tmp/vite.log 2>&1'"
ssh srv253 "docker exec yibiao-dev-workspace pkill -f 'vite --config'"

# Go 编译（在容器里）
ssh srv253 "docker exec yibiao-dev-workspace bash -c 'cd /workspace/yibiao-web && go build ./...'"

# 连数据库
ssh -L 5433:127.0.0.1:5433 srv253     # 然后本机用 127.0.0.1:5433 连
# 或直接在容器里
ssh srv253 "docker exec -it yibiao-dev-postgres psql -U yibiao -d yibiao"

# 前后端一起停
ssh srv253 "cd /opt/yibiao-dev && docker compose -f docker-compose.dev.yml down"

# 看资源占用
ssh srv253 "docker stats --no-stream yibiao-dev-workspace yibiao-dev-postgres"
```

---

## 六、当前验证状态

### 全部通过 ✅

| 验证 | 结果 | 证据 |
|---|---|---|
| 前端脱离 Electron | ✅ 通过 | 页面完整渲染，`#root` 长度 11,377，控制台 **0 错误** |
| C# 助手 Linux 编译运行 | ✅ 通过 | 81MB 单文件，stdio 协议 `{"ok":true}` |
| headless Chromium 布局测量 | ✅ 通过 | `producer = canvas-measurer`，页数 1 |

### 关键里程碑

```
✅ 服务器环境就绪（swap + 16G 空间回收）
✅ 开发镜像（Go 1.27 + Node 22 + .NET 10 + LibreOffice + 中文字体）
✅ 中文字体映射（fc-match 宋体 → Noto Serif CJK SC）
✅ PostgreSQL 17 运行中
✅ 代码同步（983 文件 / 74MB）
✅ 前端依赖（998 包，docx-editor 补丁已应用）
✅ Vite 公网可访问（HTTP 200，93ms）
✅ 前端页面渲染验证（截图见 03-feasibility-report.md）
✅ C# 助手协议验证
✅ 布局测量验证
```

### 立即可以访问

```
前端：http://<SERVER_IP>:5180/     ← 浏览器打开
```

打开会看到完整的易标界面（mock 数据，无后端）。

### 下一步

| 优先级 | 任务 |
|---|---|
| 1 | Go 服务骨架 + `/health` + PG 连接 |
| 2 | bridge 适配层 `web.ts`（把 mock 换成真实 HTTP）|
| 3 | 认证（登录/JWT）|
| 4 | 第一张表的 sqlc + CRUD，打通一条完整链路 |

### 已知的临时改动（需要时回滚）

| 文件 | 改动 | 回滚 |
|---|---|---|
| `client/index.html` | 注入 mock bridge | `cp client/index.html.bak client/index.html` |
| `client/src/shared/bridge/mock.ts` | 新增 | 删除 |
| `client/vite.web.config.ts` | 新增 | 删除 |
| `client/.yb-layout-entry/` | 布局测量打包入口（临时） | 删除 |

**注意**：`index.html` 的改动只影响 Web 验证，桌面版构建时 `window.yibiao` 由 preload 注入，`if (!window.yibiao)` 判断不会触发 mock。**桌面版行为不受影响。**

---

## 七、注意事项

### 不要动的东西

- 服务器上已有的 13 个容器（dpxwebsite、sub2api、agent-room 等）
- `/opt` 下其他目录
- 全局 npm/pip 配置

### 资源限制

`docker-compose.dev.yml` 里设了内存上限：

- dev 容器：3 G
- postgres：1 G

总共 4 G，加上已有服务约 4 G，还有 swap 兜底。**如果遇到 OOM，先看 `docker stats`，别急着加内存**。

### 磁盘监控

```bash
ssh srv253 "df -h /; docker system df"
```

构建次数多了会积累镜像层，定期清理：

```bash
ssh srv253 "docker builder prune -af"
```

（注意：这会清掉所有构建缓存，下次构建变慢，但能腾出空间。上次清出 16G。）

### 端口冲突排查

```bash
ssh srv253 "ss -tlnp | grep -E ':5180|:8090|:5433'"
```

---

## 八、安全提醒

**当前配置是开发环境，不能直接用于生产**：

| 项 | 现状 | 生产要求 |
|---|---|---|
| SSH | 端口 <SSH_PORT> + 私钥，可接受 | 保持 |
| 数据库 | 只监听 `127.0.0.1:5433` | 保持，绝不开公网 |
| 前端/API | **公网可直连 5180/8090，无认证** | 加 Nginx + HTTPS + 认证 |
| 密码 | `yibiao_dev_pwd` 明文在 compose 里 | 用 secret 管理 |
| Chromium CDP | 9222 对公网开放 | 用完关掉 |

**验证期结束后**，建议：
1. 关掉 9222：`ufw delete allow 9222/tcp`
2. 或把 5180 改成只允许你的 IP：`ufw delete allow 5180/tcp && ufw allow from <你的IP> to any port 5180`

---

## 九、故障排查

| 现象 | 排查 |
|---|---|
| 浏览器打不开 5180 | `ssh srv253 "ss -tlnp \| grep 5180"`；`ufw status`；`docker ps` |
| 页面白屏 | F12 控制台看报错；确认 mock bridge 是否注入（Console 应有 `[mock-bridge] 已注入`）|
| HMR 不生效 | 检查 vite 日志有无 WebSocket 错误；确认用的是 `vite.web.config.ts` |
| npm 装不上 | 检查磁盘 `df -h /`；`docker system df` |
| Go 编译 OOM | `free -h` 看 swap 是否生效；降低 `-p` 并发 |
| 容器起不来 | `docker logs yibiao-dev-workspace` |
