# HollowTone Server · 个人云端音乐服务器

HollowTone 的个人服务器端：为本地音乐播放器提供**云端歌曲存储**与**多设备增量同步**。单二进制、零外部依赖、纯 Go SQLite 驱动（无需 CGO），最适合跑在自己的 NAS / 小主机上。

> 设计哲学与客户端一致：不依赖中心化服务，服务器就是你自己的私有云。

---

## ✨ 能力

| 模块 | 说明 |
|------|------|
| 鉴权 | 用户名/密码注册与登录，JWT Bearer Token；首个账号可直接注册引导 |
| 音乐库元数据 | Track CRUD，GUID 稳定标识，`version` 版本号 + 最后写入优先（LWW） |
| 增量同步 | `GET /sync?since=<ms>` 返回自某时间点以来所有变更（含删除墓碑） |
| 歌曲文件存储 | 内容寻址（SHA-256）去重存储；整文件上传 + **分块断点续传** |
| 流式播放 | `Range` 支持，客户端可拖动进度条 seek |
| 封面 / 歌词 | 独立资源上传（内容寻址），也可绑定到 Track 的 `artKey` |
| 播放列表同步 | 整表条目有序替换，版本化 LWW |
| 部署 | Dockerfile + docker-compose + 一键脚本（Windows / Linux / macOS） |

---

## 🚀 快速开始

### 方式一：本地直接运行

```bash
cd server
# Windows
run.bat
# Linux / macOS
sh run.sh
```

默认监听 `:8080`，数据写入 `server/data/`。

### 方式二：Docker

```bash
cd server
cp .env.example .env        # 按需修改
docker compose up -d --build
```

数据持久化在 `server/data/`（挂载到容器 `/data`）。

### 首次使用

```bash
# 1) 注册（首个账号可直接注册）
curl -X POST http://127.0.0.1:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"me","password":"secret123"}'
# → {"token":"...","expiresAt":...,"user":{"id":1,"username":"me"}}

# 2) 之后所有请求带上
#    Authorization: Bearer <token>
```

---

## 📡 API 一览

基础路径 `/api/v1`。除 `healthz` 与 `auth/*` 外均需 `Authorization: Bearer <token>`（流式/资源下载也可用 `?token=` 查询参数，方便 `<audio>` 标签）。

### 健康检查 / 鉴权

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/healthz` | 存活与 DB 状态 |
| POST | `/api/v1/auth/register` | 注册 `{username,password}` |
| POST | `/api/v1/auth/login` | 登录 |
| GET | `/api/v1/me` | 当前用户 |

### 音乐库同步

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/sync?since=<ms>` | 拉取自 `since` 以来变更的 tracks + playlists，返回 `serverTime` |
| PUT | `/api/v1/tracks/{guid}` | 新增/更新 track（LWW） |
| GET | `/api/v1/tracks/{guid}` | 取单个 track |
| POST | `/api/v1/tracks/{guid}/file` | 整文件上传音频并绑定 |
| GET | `/api/v1/tracks/{guid}/stream` | 流式播放（支持 Range） |
| GET | `/api/v1/tracks/{guid}/art` | 流式封面 |

### 播放列表

| 方法 | 路径 | 说明 |
|------|------|------|
| PUT | `/api/v1/playlists/{guid}` | 新增/更新（条目有序整表替换，LWW） |
| GET | `/api/v1/playlists/{guid}` | 取单个 |

### 资源（封面 / 歌词）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/assets` | 上传独立资源，返回 `key` |
| GET | `/api/v1/assets/{key}` | 下载资源 |

### 分块断点续传

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/uploads/{uploadId}/init` | `{totalChunks,target}` 初始化，返回 `chunkSize` 与已收分片 |
| PUT | `/api/v1/uploads/{uploadId}/chunks/{index}` | 上传第 `index` 片（可从 0 开始，乱序亦可） |
| GET | `/api/v1/uploads/{uploadId}` | 查询已收分片（断点续传用） |
| POST | `/api/v1/uploads/{uploadId}/finalize` | `{totalChunks,target,guid?}` 合并；带 `guid` 则绑定到 Track |

---

## 🔁 同步协议（LWW + 版本号）

每个 Track / Playlist 都有单调递增的 `version`。客户端写回时携带 `baseVersion`（它上次看到的版本）：

- `baseVersion = 0` 或 `null`：视作新建 / 强制写，服务器直接接收。
- `baseVersion = 当前版本`：正常更新，`version+1`。
- `baseVersion < 当前版本`：返回 **409 Conflict**，响应体 `{"error":"conflict","current":{...当前行...}}`，客户端据此合并后重试。

删除采用**墓碑**：`PUT` 时 `deleted:true`，行保留且 `updatedAt` 更新，`/sync` 会把它下发给其他设备，让它们同步删除本地项。

**增量同步流程（客户端）**：

1. 首次 `GET /sync?since=0` 全量拉取，记录响应中的 `serverTime` 为本地水位。
2. 之后定期 `GET /sync?since=<本地水位>`，把返回的变更应用本地，再用新的 `serverTime` 更新水位。
3. 本地改动通过 `PUT /tracks/{guid}`、`PUT /playlists/{guid}` 回推；遇到 409 用 `current` 合并后以新 `baseVersion` 重试。

---

## 🗂️ 目录结构

```
server/
├── cmd/hollowtone-server/main.go   # 入口：加载配置、启动 HTTP、优雅退出
├── internal/
│   ├── config/    # 环境变量配置与默认值
│   ├── store/     # SQLite schema / 迁移 / users·tracks·playlists 持久化
│   ├── storage/   # 内容寻址对象存储 + 分块上传暂存
│   ├── auth/      # JWT 签发/校验 + 中间件
│   └── api/       # HTTP 路由与 handler
├── smoke/         # 端到端冒烟测试（go run ./smoke）
├── Dockerfile
├── docker-compose.yml
├── .env.example
├── run.bat / run.sh
└── README.md
```

## ⚙️ 配置项（环境变量）

| 变量 | 默认 | 说明 |
|------|------|------|
| `HT_ADDR` | `:8080` | 监听地址 |
| `HT_DATA_DIR` | `./data` | 数据根目录（DB / 媒体 / 封面 / 上传暂存） |
| `HT_DB_PATH` | `<data>/hollowtone_server.sqlite` | SQLite 路径 |
| `HT_MEDIA_DIR` | `<data>/media` | 音频对象目录 |
| `HT_ART_DIR` | `<data>/art` | 封面/歌词对象目录 |
| `HT_JWT_SECRET` | 自动生成并存 `<data>/.jwt_secret` | Token 签名密钥，生产建议固定 |
| `HT_TOKEN_TTL` | `720h` | Token 有效期 |
| `HT_ALLOW_REGISTER` | `true` | 是否开放注册（首个账号始终可注册） |
| `HT_MAX_UPLOAD_BYTES` | `2147483648` (2 GiB) | 单次整文件上传上限 |
| `HT_CHUNK_SIZE` | `8388608` (8 MiB) | 分块上传建议分片大小 |

## 🧪 测试

```bash
# 启动服务器后
go run ./smoke -base http://127.0.0.1:8080
```

覆盖：注册 → 登录 → 建 track → LWW 冲突(409) → 分块上传 → 合并 → 流式下载字节一致 → Range(206) → 播放列表 → 资源上传 → 全量/增量同步 → 删除墓碑。

## 🔒 安全说明

- 密码使用 bcrypt 哈希存储。
- Token 为 HS256 JWT；`HT_JWT_SECRET` 请在生产环境设置为足够长的随机串。
- 服务默认 HTTP 明文，公网部署请前置 TLS 反向代理（Caddy / Nginx）。
- 内容寻址存储天然去重；同名不同内容不会互相覆盖。

---

## 📱 客户端接入（Flutter）

客户端已内置云同步，接入点如下：

| 文件 | 职责 |
|------|------|
| `lib/logic/cloud_api.dart` | `HollowToneApi`：强类型封装登录/注册/同步/上传/流式 URL |
| `lib/logic/sync_service.dart` | `SyncService`：`sync_map` 映射表、推送/拉取/409 合并/墓碑，全程用原生 SQL，不改 drift 生成代码 |
| `lib/providers/cloud_sync_provider.dart` | `cloudSyncProvider`（手写 NotifierProvider）：凭证与水位持久化、`login/register/logout/syncNow` |
| `lib/ui/screens/settings_screen.dart` | 设置页「云端同步」卡片：服务器地址/账号/密码、登录/注册、立即同步、启动自动同步 |
| `lib/providers/remote_provider.dart` | 播放器协议解析支持 `hollowtone://cloud/<guid>`（带 token 的流地址） |
| `lib/main.dart` | 启动 2s 后若已配置且开启自动同步则静默同步 |

**数据流**

1. 设置页登录 → 服务器返回 token 存入 SharedPreferences。
2. 本地任意编辑（改元数据/歌词/封面、增删曲目与播放列表）会把对应行标记为 dirty（`sync_map.dirty=1`）。
3. `syncNow()`：先 `push` 推送所有 dirty 行（`baseVersion` 落后于服务器则收到 409 并采用服务器版本），再按水位 `pull` 拉取远端变更；远端新曲目以 `hollowtone://cloud/<guid>` 路径入库，播放时实时换取带 token 的流地址。
4. 删除本地曲目/播放列表时先推墓碑，其它设备下次同步即删除对应项。

**注意**

- 后端 GUID 由客户端生成（`track-<微秒>-<随机>`），因此两端无需共享自增主键即可收敛。
- 云来源曲目只保存元数据与文件引用；实际音频通过 `GET /tracks/{guid}/stream` 流式播放，随播随取，不落本地缓存（除非你另行实现下载）。
