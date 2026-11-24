# OpenMusic Meting API Go

[OpenMusic-Meting-API](https://github.com/qq01-hub/OpenMusic-Meting-API) 的 Go 迁移版本，基于上游提交 [fb9030f](https://github.com/qq01-hub/OpenMusic-Meting-API/tree/fb9030ff2b8ab295c213f8542807c1a95e6cf540)。Go 服务以 `/api` 为公共入口，支持原生网易云和四平台兼容部署。

| 功能 | 独立 Go | 兼容部署 |
| --- | --- | --- |
| 网易云歌曲、歌单、搜索、歌手、电台、封面、普通和结构化歌词 | Go | Go |
| 网易云播放地址和私人漫游 | Go，使用本地账号配置 | Node.js，使用原账号池和辅助服务 |
| 网易云房间私有请求 | Go | Node.js |
| QQ 音乐、汽水音乐、酷狗音乐 | 需要兼容部署 | Node.js |
| 原管理后台、扫码、2FA、Cookie 校验、监测、备份 | 需要兼容部署 | Node.js |
| OpenMusic 令牌及签名 | Go | Node.js 的现有令牌、授权开关与防重放存储 |
| `/livez`、`/readyz` | Go | Go |

## 独立 Go

需要 Go 1.26 或更新版本。镜像和 CI 使用 Go 1.27.4。

```sh
export METING_API_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/meting
```

默认监听 `127.0.0.1:3000`。`GET /livez` 和 `GET /readyz` 可直接访问。以下命令使用完整签名请求搜索网易云歌曲：

```sh
python3 - <<'PY'
import base64, hashlib, hmac, json, os, secrets, time, urllib.request

def b64url(value):
    return base64.urlsafe_b64encode(value).decode().rstrip("=")

token = os.environ["METING_API_TOKEN"]
target = "/api?server=netease&type=search&id=%E6%B5%B7%E9%98%94%E5%A4%A9%E7%A9%BA&limit=3"
claims = {"v": 1, "sub": "openmusic:system", "purpose": "system",
          "ts": int(time.time()), "nonce": b64url(secrets.token_bytes(18))}
assertion = b64url(json.dumps(claims, separators=(",", ":")).encode())
payload = "\n".join([assertion, "GET", target, hashlib.sha256(b"").hexdigest()])
signature = b64url(hmac.new(token.encode(), payload.encode(), hashlib.sha256).digest())
req = urllib.request.Request("http://127.0.0.1:3000" + target, headers={
    "Authorization": "Bearer " + token,
    "X-OpenMusic-Assertion": assertion,
    "X-OpenMusic-Signature": signature,
})
with urllib.request.urlopen(req, timeout=50) as reply:
    print(reply.read().decode())
PY
```

播放和漫游可使用网易云账号配置文件。将配置保存在权限为 `0600` 的文件里，并通过 `NETEASE_ACCOUNTS_FILE` 指定它。会员档位由操作者填写，播放仍由平台校验实际权益。

```json
[
  {"cookie":"MUSIC_U=<账号凭证>","membership":"vip","fmPriority":true}
]
```

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `3000` | 默认回环地址的端口 |
| `LISTEN_ADDR` | `127.0.0.1:$PORT` | 完整监听地址。容器为 `:3000` |
| `METING_API_TOKEN` | 必填 | 独立模式的 API Token，至少 32 个非空白字符 |
| `OPENMUSIC_AUTH_ENABLED` | `true` | 独立模式的短期断言开关。关闭后仍校验 Bearer |
| `NETEASE_ACCOUNTS_FILE` | 空 | 独立模式的账号 JSON，启动时载入 |
| `PUBLIC_BASE_URL` | 当前服务地址 | 生成资源链接时使用的公开基址，可含子路径 |
| `REQUEST_TIMEOUT` | `45s` | Go 路由整次请求的时限 |
| `NETEASE_DJ_PAGE_DELAY_MS` | `400` | 网易云电台请求分页之间的毫秒数 |
| `LEGACY_URL` | 空 | 使用兼容模式时的 Node.js 服务地址 |
| `LEGACY_BRIDGE_KEY` | 空 | Go 与兼容服务共用的内部密钥 |

## 兼容部署

```sh
cp .env.example .env
# 填写 LEGACY_BRIDGE_KEY。用 openssl rand -hex 32 生成。
# 新数据目录还需填写 ADMIN_PASSWORD。
docker compose up -d --build
curl -fsS http://127.0.0.1:3000/readyz
```

Compose 只映射 Go 服务。后台默认入口为 `http://127.0.0.1:3000/admin`，原管理后台仍负责生成 API Token、维护 Cookie 和配置授权。相同容器网络内可使用 `http://meting:3000` 访问 Go 服务。

迁移既有部署时，先停止旧服务并完整备份 `DATA_DIR`，然后将备份副本配置为 `LEGACY_DATA_DIR` 做验证。需要一并保留 Cookie 加密密钥。如从环境变量读取密钥，兼容容器也需要相同的 `METING_COOKIE_ENCRYPTION_KEY)。如通过 `METING_COOKIE_KEY_FILE` 指定路径，文件必须位于已挂载目录，并使用容器内路径。Go 进程无需挂载这个目录。兼容容器默认使用 UID 和 GID 1010，可用 `LEGACY_UID`、`LEGACY_GID` 匹配已有目录权限。验收后可在同一套 OpenMusic 配置中切换地址，沿用现有 Token。

若已有可访问的原版 Node.js 服务，只配置 `LEGACY_URL` 也能启动普通反向代理。要启用网易云原生分流，Node.js 需运行随仓库提供的 `compat/bridge.mjs`，并在两个进程配置相同的 `LEGACY_BRIDGE_KEY`。兼容进程应位于 Go 服务可访问的私有网络。

## 验证

```sh
go vet ./...
go test -race ./...
go build -o bin/meting ./cmd/meting
node --check compat/bridge.mjs
```

[详细实现和兼容差异](docs/IMPLEMENTATION.md) · [上游 OpenMusic 授权协议](https://github.com/qq01-hub/OpenMusic-Meting-API/blob/fb9030ff2b8ab295c213f8542807c1a95e6cf540/docs/OPENMUSIC_AUTH.md)
