# HollowTone Server · 预编译二进制

本分支（`release`）只存放**可直接运行的成品二进制**，源码见 `master` 分支。

## 当前版本：v0.2.0

构建信息：

| 项 | 值 |
|---|---|
| 版本 | `v0.2.0` |
| 源码 commit | `db99041c9db9` |
| 构建时间 | 2026-11-01T05:17:45Z |
| Go 版本 | go1.26.10 |
| CGO | 关闭（静态二进制，无外部依赖） |

## 下载

| 文件 | 适用平台 | 大小 |
|---|---|---|
| `hollowtone-server-linux-amd64` | Linux x86_64（NAS / 小主机 / 云服务器） | 9.9 MB |
| `hollowtone-server-linux-arm64` | Linux ARM64（树莓派 4/5、ARM 云主机） | 9.3 MB |
| `hollowtone-server-windows-amd64.exe` | Windows x64 | 10.1 MB |

> 命名变化：v0.1.0 的 Windows 产物叫 `hollowtone-server.exe`，从 v0.2.0 起统一为
> `<名称>-<系统>-<架构>` 形式，便于多平台共存。

## 校验

```bash
sha256sum -c SHA256SUMS.txt
```

## 运行

Linux：

```bash
chmod +x hollowtone-server-linux-amd64
HT_DATA_DIR=./data HT_ADDR=:8080 ./hollowtone-server-linux-amd64
```

Windows（PowerShell）：

```powershell
$env:HT_DATA_DIR = ".\data"
$env:HT_ADDR = ":8080"
.\hollowtone-server-windows-amd64.exe
```

确认跑的是哪个版本：

```bash
curl -s http://127.0.0.1:8080/version
# {"version":"v0.2.0","commit":"db99041c9db9",...}
```

## v0.2.0 变更摘要

**安全**

- 对象键白名单校验：`/assets/{key}` 等下载端点只接受 64 位十六进制内容地址，
  `../` 构造一律 404，无法越出对象目录读取任意文件（此前可读 `.jwt_secret`）。
- 登录 / 注册按客户端限流：5 分钟内最多 10 次失败，超出返回 `429` + `Retry-After`。
- 分块上传增加**单会话总量上限**，超过 `HT_MAX_UPLOAD_BYTES` 返回 `413`。
- `X-Forwarded-For` 仅在显式开启 `HT_TRUST_PROXY` 时采信。

**正确性**

- 移除 `tracks(user_id, content_hash)` 的唯一约束：同一音频被两条曲目引用时，
  第二次绑定不再返回 500。
- JSON 请求体忽略未知字段，客户端升级不再被服务端卡住。

**性能**

- 日志中间件透传 `io.ReaderFrom` / `Flush`，音频流仍走 `sendfile` 快路径。
- 内容寻址对象现在返回强 `ETag` 与 `Cache-Control: immutable`，客户端可 304 命中。
- `Content-Type` 改为嗅探真实字节；此前封面一律返回 `application/octet-stream`。

**运维**

- 新增 `GET /version`，启动日志打印版本横幅。
- `/healthz` 在数据库不可达时返回 `503`。
- 清理协程自动删除过期未完成上传（`HT_UPLOAD_TTL` / `HT_JANITOR_INTERVAL`）。
- 可选 CORS（`HT_CORS_ORIGINS`），供 Flutter Web 构建使用。
- 新增跨平台发布脚本与单元测试。

## 配置项

完整说明见源码仓库 `master` 分支的 `.env.example`。
