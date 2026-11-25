# OpenMusic Meting API Go

[OpenMusic-Meting-API](https://github.com/qq01-hub/OpenMusic-Meting-API) 的 Go 移植版，原生接入网易云、QQ 音乐、酷狗和汽水音乐。可选的 Node.js 兼容服务保留原版后台及其工作流。

[English](./README.md) · [发行版本](https://github.com/nova1751/openmusic-meting-api-go/releases) · [迁移指南](./docs/MIGRATE_NODE_TO_GO.md)

## 部署模式

| 功能 | 独立 Go | Go + Node.js 兼容部署 |
| --- | --- | --- |
| 网易云歌曲、歌单、专辑、搜索、歌手、电台、封面、普通及结构化歌词 | Go | Go |
| 四平台曲库、取链和漫游 | Go，取链和漫游依赖授权账号与上游配置 | Node.js |
| 房间私有音乐和漫游 | Go | Node.js |
| 汽水音频源信息、解密和范围读取 | Go | Node.js |
| Cookie 管理、扫码、API Token、双重验证和快照 | 原生后台，汽水扫码需要 Chromium 和 SDK 资源 | 原版后台 |
| 交互式验证、多用户、共享账号、监测和 Webhook | Go，验证需要 Chromium 和上游实际下发的挑战 | Node.js |
| OpenMusic 令牌和请求签名 | Go | 使用原 Node.js 令牌和签名设置 |
| 存活与就绪检查 | `/livez` 和 `/readyz` | `/livez` 和 `/readyz` |

## 快速开始

从源码构建需要 Go 1.26 或更高版本。容器构建和 CI 使用 Go 1.27.4。

```sh
git clone https://github.com/nova1751/openmusic-meting-api-go.git
cd openmusic-meting-api-go
```

**POSIX Shell（需要 OpenSSL）**

```sh
export METING_API_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/meting
```

**PowerShell**

```powershell
$tokenBytes = [byte[]]::new(32)
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
try { $rng.GetBytes($tokenBytes) } finally { $rng.Dispose() }
$env:METING_API_TOKEN = [System.BitConverter]::ToString($tokenBytes).Replace('-', '').ToLowerInvariant()
go run ./cmd/meting
```

服务默认监听 `127.0.0.1:3000`。在另一个终端运行 `curl -fsS http://127.0.0.1:3000/readyz` 检查就绪状态。PowerShell 下可用 `Invoke-RestMethod http://127.0.0.1:3000/readyz`。两个健康检查端点均可直接访问。

### 签名请求

`/api` 默认要求 Bearer 令牌和已签名的 OpenMusic 断言。在设有相同 `METING_API_TOKEN` 的终端运行以下 Python 3 示例，即可搜索网易云歌曲。签名使用的路径和查询字符串须与实际发送的内容完全一致。

```sh
python3 - <<'PY'
import base64
import hashlib
import hmac
import json
import os
import secrets
import time
import urllib.parse
import urllib.request

def b64url(value):
    return base64.urlsafe_b64encode(value).decode().rstrip("=")

token = os.environ["METING_API_TOKEN"]
query = urllib.parse.urlencode({
    "server": "netease", "type": "search", "id": "海阔天空", "limit": 3,
})
target = "/api?" + query
claims = {
    "v": 1, "sub": "openmusic:system", "purpose": "system",
    "ts": int(time.time()), "nonce": b64url(secrets.token_bytes(18)),
}
assertion = b64url(json.dumps(claims, separators=(",", ":")).encode())
payload = "\n".join([assertion, "GET", target, hashlib.sha256(b"").hexdigest()])
signature = b64url(hmac.new(token.encode(), payload.encode(), hashlib.sha256).digest())
request = urllib.request.Request("http://127.0.0.1:3000" + target, headers={
    "Authorization": "Bearer " + token,
    "X-OpenMusic-Assertion": assertion,
    "X-OpenMusic-Signature": signature,
})
with urllib.request.urlopen(request, timeout=50) as response:
    print(response.read().decode())
PY
```

如果只想验证本地签名，把示例中的目标改为 `target = "/test"`。该端点直接返回 `{"api":"/api","status":"ok"}`，无需访问网易云。

请将令牌保留在服务端。签名和请求规则见 [OpenMusic 授权协议](https://github.com/qq01-hub/OpenMusic-Meting-API/blob/fb1d08dcf81ea1f63a75fb344f7a60b49492d9f5/docs/OPENMUSIC_AUTH.md)，响应格式和兼容差异见 [实现说明](./docs/IMPLEMENTATION.md)。

### 配置

独立模式下，播放和私人漫游可通过 `NETEASE_ACCOUNTS_FILE` 读取网易云账号凭证。将凭证保存在 JSON 文件中，在 Unix 上将文件权限设为 `0600`。`membership` 按实际会员档位填写 `free`、`vip` 或 `svip`，播放权益由网易云校验。最多可为一个账号启用 `fmPriority`。

```json
[
  {"cookie": "MUSIC_U=<账号凭证>", "membership": "vip", "fmPriority": true}
]
```

QQ、酷狗、汽水使用同样的数组格式，分别由 TENCENT_ACCOUNTS_FILE、
KUGOU_ACCOUNTS_FILE 和 QISHUI_ACCOUNTS_FILE 加载。汽水取链沿用旧配置中的
签名服务，也可通过 QISHUI_SIGNER_URL 设置。

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `PORT` | `3000` | 未设置 `LISTEN_ADDR` 时使用的端口 |
| `LISTEN_ADDR` | `127.0.0.1:$PORT` | 完整监听地址。容器内使用 `:3000`。 |
| `METING_API_TOKEN` | 独立模式必填，或使用旧令牌 | 至少 32 个非空白字符的 API 令牌 |
| `OPENMUSIC_AUTH_ENABLED` | `true` | 独立模式是否要求签名断言。设为 `false` 后仍校验 Bearer 令牌。 |
| `NETEASE_ACCOUNTS_FILE` | 未设置 | 独立模式启动时加载的账号 JSON 文件 |
| `TENCENT_ACCOUNTS_FILE`、`KUGOU_ACCOUNTS_FILE`、`QISHUI_ACCOUNTS_FILE` | 未设置 | 其他平台账号文件 |
| `QISHUI_SIGNER_URL`、`LOUDNESS_SERVICE_URL` | 未设置 | 外置服务地址，已保存的旧配置优先 |
| `QISHUI_SECURITY_ASSETS_DIR`、`QISHUI_CHROMIUM_PATH` | 未设置 | 汽水扫码的已有 SDK 目录和可选 Chromium 路径 |
| `METING_NODE_DATA_DIR` | 未设置 | 只读加载旧 Node 数据目录中的加密账号；不能与独立账号文件同时使用 |
| `METING_COOKIE_KEY_FILE` | 旧数据目录内的密钥文件 | 旧版 Cookie 加密密钥路径 |
| `METING_COOKIE_ENCRYPTION_KEY` | 未设置 | 旧版使用的环境变量密钥；优先于密钥文件 |
| `METING_NODE_TOKEN_NAME` | 未设置 | 未设置 `METING_API_TOKEN` 时读取指定名称的旧 Token；撤销后请求立即失败 |
| `METING_NATIVE_ADMIN` | `false` | 启用原生管理页；API 必须只监听回环地址 |
| `METING_SITE_GATEWAY_CONFIG` | 未设置 | 可选的网站网关 JSON 配置文件 |
| `METING_SITE_GATEWAY_ADDR` | `127.0.0.1:3358` | 网站网关监听地址；只允许回环地址 |
| `MUSIC_ORIGIN_SECRET` | 未设置 | 网站网关与可信边缘代理共享的密钥，至少 32 字节 |
| `PUBLIC_BASE_URL` | 当前请求的协议和主机 | 生成资源链接时使用的公开基址，可包含子路径。 |
| `REQUEST_TIMEOUT` | `45s` | Go 处理请求的总时限 |
| `NETEASE_DJ_PAGE_DELAY_MS` | `400` | 网易云电台分页请求之间的间隔，单位为毫秒 |
| `LEGACY_URL` | 未设置 | 可选 Node.js 服务的内网地址 |
| `LEGACY_BRIDGE_KEY` | 未设置 | Go 服务与 Node.js 桥接服务共用的内部密钥 |

`METING_NATIVE_ADMIN=true` 需要 HTTPS 公共源站、回环监听和含现有管理员的
旧数据副本。全新部署可用 ADMIN_PASSWORD 和可选的 ADMIN_USERNAME 初始化。登录兼容旧版和 scrypt 密码。QQ 续期需要原 Cookie 中的设备档案和续期凭据。
汽水扫码与二次验证依赖 Chromium 和现有 SDK 资源。QQ 官方验证窗口也需要 Chromium。
这两种交互式验证和会员取链需要使用实际账号验收，详见迁移指南。

首次登录生成独立的 Go 密码文件。回滚使用旧 Node 用户文件中的密码。
每个数据目录同一时间由一个进程写入。

原生管理页遵循旧数据中的备份开关：启动时、账号状态变更后和本地每天零点创建
受限权限快照，最近 7 份保存在 `go-native-backups`。如果 Cookie 密钥通过
环境变量注入，必须单独安全备份环境配置。

网站网关仅允许配置中的歌单、专辑和相应媒体 ID，并校验来源密钥及 Origin。
仍需在边缘代理实施限流；Origin 可被非浏览器客户端伪造，公开播放链接也可能
被复制。配置与安全边界见 [迁移指南](./docs/MIGRATE_NODE_TO_GO.md)。

## Node.js 兼容部署

将 `.env.example` 复制为 `.env`，用 `openssl rand -hex 32` 生成随机值填入 `LEGACY_BRIDGE_KEY`。如果使用全新的数据目录，还要填写 `ADMIN_PASSWORD`。

```sh
cp .env.example .env
# 启动前填好 .env。
docker compose up -d --build --wait
curl -fsS http://127.0.0.1:3000/readyz
```

Compose 将 Go 服务映射到 `127.0.0.1:3000`。原管理后台默认位于 `http://127.0.0.1:3000/admin`，继续负责令牌、Cookie 和授权设置。同一 Compose 网络内的容器可以通过 `http://meting:3000` 访问 Go 服务。

已有部署的备份、并行验证、切流和回滚步骤见 [迁移指南](./docs/MIGRATE_NODE_TO_GO.md)。

若已有上游 Node.js 服务，只设置 `LEGACY_URL` 即可启用反向代理。要由 Go 处理其支持的网易云公开请求，请在 Node.js 服务运行 `compat/bridge.mjs`，并为两个服务设置相同的 `LEGACY_BRIDGE_KEY`。Node.js 服务应部署在 Go 可以访问的私有网络内。

## 预编译发行包与容器镜像

[GitHub Releases](https://github.com/nova1751/openmusic-meting-api-go/releases) 提供 Linux、macOS 和 Windows 的 AMD64、ARM64 发行包。将发行包和 `SHA256SUMS` 下载到同一目录，在有 GNU coreutils 的环境中运行 `sha256sum -c --ignore-missing SHA256SUMS` 校验。解压后可运行 `meting --version` 确认版本。

发行页还提供 Linux AMD64 和 ARM64 的 Go 容器归档。按 `SHA256SUMS.docker` 校验后，用 `docker load -i <归档文件>` 导入。要从 GHCR 部署同版本的 Go 和 Node.js 镜像，先按上节配置 `.env`，再设置版本。

```sh
export METING_VERSION=v0.1.0
docker compose -f compose.yaml -f compose.release.yaml pull
docker compose -f compose.yaml -f compose.release.yaml up -d --no-build --wait
```

GHCR 容器包为私有时，需要登录后才能拉取。公开包支持匿名拉取。参见 [GitHub Packages 可见性和访问权限](https://docs.github.com/zh/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)。

## 开发

```sh
go vet ./...
go test -race ./...
go build ./cmd/meting
node --check compat/bridge.mjs
```

发布工作流根据版本标签构建并校验发行包，然后发布两张 Linux 容器镜像。本地构建 Docker 归档需要支持 Docker 导出器的 Buildx 构建器。

```sh
./scripts/build-release.sh v0.1.0
docker buildx create --name meting-release --driver docker-container
BUILDX_BUILDER=meting-release ./scripts/build-docker-release.sh v0.1.0
```

## 致谢

Go 移植版由 [nova1751](https://github.com/nova1751) 维护，沿用了 [OpenMusic-Meting-API 的 `fb1d08d` 版本](https://github.com/qq01-hub/OpenMusic-Meting-API/tree/fb1d08dcf81ea1f63a75fb344f7a60b49492d9f5) 的接口约定和部分网易云请求格式。上游软件包标注的作者为 xizeyoupan，并声明采用 MIT 许可证。本项目是独立的社区项目，与上游项目和所支持的音乐平台均无隶属关系。上游和第三方署名见 [NOTICE.md](./NOTICE.md)。

## 许可证

Go 实现采用 [MIT 许可证](./LICENSE)。可选的 Node.js 兼容镜像包含上游从 Folia 派生的 QRC 实现，继续保留其 [AGPL-3.0 许可证和署名](./NOTICE.md)。
