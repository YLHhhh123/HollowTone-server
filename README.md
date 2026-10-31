# HollowTone Server - Release Binaries

本分支（`release`）仅存放 HollowTone 云同步服务器的编译产物，源码见 `master` 分支。

## 版本 v0.1.0

| 文件 | 平台 | 说明 |
|---|---|---|
| `hollowtone-server.exe` | Windows x64 | Windows 可执行文件 |
| `hollowtone-server-linux-amd64` | Linux x64 | 服务器/容器常用 |
| `hollowtone-server-linux-arm64` | Linux ARM64 | 树莓派/ARM 服务器 |
| `SHA256SUMS.txt` | - | 各文件 SHA-256 校验值 |

## 使用

```bash
# Linux
chmod +x hollowtone-server-linux-amd64
./hollowtone-server-linux-amd64

# Windows
hollowtone-server.exe
```

配置项参考源码仓库 `.env.example`。
