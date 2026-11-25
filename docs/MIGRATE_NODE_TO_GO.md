# 从 Node.js 迁移到 Go

## 面向网站的独立 Go 迁移

独立版接入四平台的曲库、歌词、取链和漫游。取链和漫游需使用授权账号或签名服务。
原生后台支持旧管理员、账号、API Token、用户角色、共享账号、扫码、监测和 Webhook。
QQ 官方验证与汽水二次验证需在上游下发挑战时验收。迁移前先在数据副本逐项检查。

1. 按下文备份旧数据目录、Cookie 加密密钥、服务配置与反向代理配置。
   测试时使用一份独立数据副本和独立端口，不要让 Node 与原生管理页同时写同一目录。
2. 在受限的服务环境文件中设置 `METING_NODE_DATA_DIR` 为副本路径。Go 会在内存
   中解密账号，不产生明文 Cookie 文件。如果旧 Token 只供网站网关使用，可设置
   `METING_NODE_TOKEN_NAME=site-gateway`，沿用指定的 Token。未指定名称时可使用 `api_tokens.json` 中的令牌，撤销立即生效。
   也可配置独立的 `METING_API_TOKEN`。环境变量 `OPENMUSIC_AUTH_ENABLED` 优先于旧配置。
3. 若需要原生管理页，设置 `METING_NATIVE_ADMIN=true`、HTTPS `PUBLIC_BASE_URL`
   和回环 `LISTEN_ADDR`。管理页沿用现有管理员登录，并支持设置和校验 2FA；首次登录
   创建 `go-admin-auth.json` 保存更强的密码哈希，不覆盖旧版用户文件。二维码
   在服务器生成，不发给第三方二维码服务。管理页不应直接暴露到公网。
4. 如需网站网关，参考 [示例](./site-gateway.example.json) 按实际站点和歌单
   创建配置文件，并设置 `METING_SITE_GATEWAY_CONFIG`、`MUSIC_ORIGIN_SECRET`
   和回环 `METING_SITE_GATEWAY_ADDR`。保留 Cloudflare Worker 的来源校验和限流、
   Nginx 的源站限制；网关本身并不提供分布式限流。
5. 验证已允许歌单、专辑、歌词、真实会员取链、原有后台登录及四平台扫码。
   使用测试账号验证 QQ 续期、官方验证和汽水二次验证。检查原有共享、Webhook、Token
   撤销、非允许来源及非允许媒体 ID 的拒绝行为。验证无误后切换反代，保留回滚入口。

启用原生管理页后，原有备份开关继续生效。快照位于数据目录的
`go-native-backups`，保留最近 7 份；文件使用受限权限。若 Cookie 加密密钥
来自环境变量，快照不包含它，必须另外备份环境配置。账户修改后需保留新数据，
再决定如何回滚；原 Node 服务不识别 Go 管理员的增强哈希文件，回滚后使用旧版密码。

容器的默认监听地址为 `:3000`。原生管理页与网站网关要求回环监听；
在 Linux 容器中可使用 host 网络并明确设置回环地址，仅暴露给本机反向代理。

原版 Node.js 部署支持完整的四个平台和管理后台。需要保留所有原版功能时请选择兼容部署：Go 接管公共入口及网易云只读查询，Node.js 子服务继续使用原账号池与持久化格式。仅需要原生支持的功能时可选择独立 Go 模式。

| 部署方式 | 现有数据 | 可用能力 |
| --- | --- | --- |
| 兼容部署 | 直接挂载旧 `DATA_DIR` 的完整副本，沿用 Cookie、Token、后台用户和配置 | 四平台、后台、监测、备份、扫码、汽水音频代理 |
| 独立 Go | 配置 API Token 和账号 JSON，或导入旧版加密账号数据 | 网易云和部分 QQ 音乐功能、可选的精简账号管理页和网站网关；不包含完整原版后台 |

## 迁移四平台部署

### 1. 记录旧配置并构建镜像

记录正在运行的 Node.js 版本、镜像标签或提交、`DATA_DIR`、调用地址和监听端口，并保留旧启动配置。Node.js 从工作目录中的 `./data` 读取默认数据，原版 Docker 的默认容器路径为 `/app/data`。如果已有 Docker 容器，可用下面的命令查看挂载。

```sh
docker inspect --format '{{range .Mounts}}{{println .Type .Name .Source .Destination}}{{end}}' openmusic-meting-api
```

在 Go 仓库中创建 `.env`。文件权限应允许当前部署用户读写，内部桥接密钥可用 `openssl rand -hex 32` 生成。尝试迁移时先用独立端口。下面的配置以 Linux 宿主机为例。

```dotenv
PORT=3001
LEGACY_DATA_DIR=/opt/openmusic-meting-go/data-stage
LEGACY_BRIDGE_KEY=<随机密钥>
LEGACY_UID=1010
LEGACY_GID=1010
```

若旧版通过 `METING_COOKIE_ENCRYPTION_KEY` 注入密钥，保留同一密钥。若使用 `METING_COOKIE_KEY_FILE`，备份该文件并将副本放进迁移数据目录，使用容器内路径，例如 `/app/data/keys/cookie.key`。保留仅在旧环境变量中设置的 `QISHUI_SIGNER_URL`、`LOUDNESS_SERVICE_URL`、`OVERSEAS` 和 `ADMIN_PATH`。已保存在后台的设置从数据目录载入。旧环境使用 `localhost` 访问的辅助服务，需要改成兼容容器可访问的 DNS 名或地址。

使用源码构建时，先在停机窗口前拉取并构建两张镜像。

```sh
docker compose build
```

使用已发布的镜像时，按 README 的发行说明加载 `compose.release.yaml`，设置目标 `METING_VERSION` 并提前拉取两张镜像。

### 2. 停止旧进程并备份完整数据

用原有的服务管理方式停止旧实例。Docker 可用 `docker stop openmusic-meting-api`，systemd 可用原服务单元的 `systemctl stop`。操作前记下原启动命令或 Compose 服务名。

对于宿主机目录或 bind mount，从旧数据目录生成备份，再复制到专用的迁移目录。

```sh
old_data=/opt/meting-api/data
backup_dir="$(sudo mktemp -d /var/tmp/openmusic-meting-backup.XXXXXXXX)"
stage_dir=/opt/openmusic-meting-go/data-stage

sudo cp -a "$old_data/." "$backup_dir/"
sudo install -d -m 0700 "$stage_dir"
sudo cp -a "$backup_dir/." "$stage_dir/"
sudo chown -R 1010:1010 "$stage_dir"
```

将路径与 UID/GID 换成实际值。只对本次建立的数据副本调整所有权。使用 Docker 命名卷时，也可以从已停止但保留的旧容器中导出。将容器名和容器内的数据路径换成实际值。

```sh
backup_dir="$(mktemp -d /var/tmp/openmusic-meting-backup.XXXXXXXX)"
stage_dir=/opt/openmusic-meting-go/data-stage
docker cp "openmusic-meting-api:/app/data/." "$backup_dir/"
sudo install -d -m 0700 "$stage_dir"
sudo cp -a "$backup_dir/." "$stage_dir/"
sudo chown -R 1010:1010 "$stage_dir"
```

备份需包含整个数据目录和实际的 Cookie 加密密钥。旧版目录可能包含 `cookies.json`、`api_tokens.json`、`users.json`、`config.json`、`security.json`、日志和日常备份。把外置密钥、旧配置和备份一起保存在受限位置。

如果需要恢复旧服务以缩短停机，可以在备份完成后先启动它，同时让兼容部署只使用独立的数据副本和测试端口。最终切换前再停旧服务，做一次最新快照，并将兼容部署切换到该副本。

### 3. 启动兼容部署并验收

```sh
docker compose up -d --wait
docker compose ps
curl -fsS http://127.0.0.1:3001/readyz
```

1. 访问原管理页面路径。检查原管理员和 2FA、四平台 Cookie、FM 优先账号、监测设置及已有 API Token。在复制的数据上手工验证时，按需重新登录后台。
2. 将 OpenMusic 的测试连接指向 Go 入口，沿用原 API Token。发起一次签名搜索、单曲查询与取链。兼容模式使用 Node.js 的授权开关，重新签名后的请求应可通过，重放原签名应得到 `401 OPENMUSIC_REPLAY`。如果只手动测试，可使用项目 README 的签名示例，并把目标端口改成本次测试端口。
3. 按启用的平台测试查歌、歌词、会员取链和 FM。汽水部署另测签名服务与带 `Range` 的音频请求。房间私有账号也应单独验证。
4. 使用反向代理时，检查响应里的播放、歌词和封面地址。若客户端依赖特定域名或前缀，设置 `PUBLIC_BASE_URL`。验签使用服务收到的路径和查询字符串，网关与 OpenMusic 需使用一致的编码和路径。避免代理改写参与签名的查询串。

若 OpenMusic 在另一个容器网络中，连接该网络的应是 Go 服务。可在部署侧添加 Compose override，下面的 Compose override 会将 `meting` 加入现有的外部网络。

```yaml
services:
  meting:
    networks: [default, openmusic]
networks:
  openmusic:
    external: true
    name: openmusic_default
```

同网络中的 OpenMusic 可以使用 `http://meting:3000`，宿主机反向代理则使用发布的回环端口。

### 4. 切流和回滚

切换时停止旧实例。若试跑期间旧实例继续提供服务，应备份最新状态，并用新的数据副本启动兼容部署。切换反代目标或 OpenMusic 的服务地址。若复用旧宿主机端口，把 `.env` 中的 `PORT` 改为旧值，再执行 `docker compose up -d --wait`。以实际生产地址复测签名搜索、取链、后台以及已接入的媒体功能。

出现故障时，停止兼容部署，把流量切回旧 Node.js 服务，使用原配置、密钥和原数据目录或切换前备份。若切换后修改过账号、Token 或后台设置，先保存兼容实例的新数据副本，再决定需要带回旧实例的变更。每份可写数据目录在同一时刻由一个 Node.js 进程使用。回滚后重跑签名和播放验收。

## 迁移到独立 Go

独立 Go 使用平台账号 JSON，或按本文开头的说明导入旧数据副本。部署前先在独立端口验收。


1. 下载与系统架构对应的发行包，并比对发行页的 `SHA256SUMS`。从源码构建可运行 `CGO_ENABLED=0 go build -trimpath -o bin/meting ./cmd/meting`。
2. 给 `METING_API_TOKEN` 配置一个仍安全的原 OpenMusic Token，或者生成新 Token，并同步更新 OpenMusic 服务端。默认启用短期断言。公网访问请通过受控的反向代理或私有网络。
3. 需要账号时，从可信的授权来源取得有效 Cookie，按 README 格式写入对应平台的账号文件。填写对应的 `free`、`vip` 或 `svip` 权益，按需指定一个 FM 优先账号。为账号文件和含 Token 的部署配置设置最小读取权限。
4. 先在额外端口以原客户端验证，再切换流量。配置可以放进现有服务管理器的受限环境文件。例如保留签名，并设置 `LISTEN_ADDR=127.0.0.1:3001`，随后运行 `./meting`。用 `/readyz` 探活，并测试真实账号的播放和 FM。

汽水取链使用旧配置 qishuiSignerUrl 或 QISHUI_SIGNER_URL。试跑原生汽水扫码时，
安装 Chromium，并将 QISHUI_SECURITY_ASSETS_DIR 指向原部署的
src/providers/qishui/security。可通过 QISHUI_CHROMIUM_PATH 指定浏览器路径。
Go 默认容器镜像不含 Chromium，可从宿主机运行 Go 程序或使用安装了浏览器的派生镜像。

## 常见问题

| 表现 | 检查 |
| --- | --- |
| 兼容子服务无法启动或提示初始化管理员 | 检查挂载是否指向包含 `users.json` 的实际旧数据目录，并检查容器 UID/GID 的读写权限 |
| 后台能登录，Cookie 解密失败 | 核对外置密钥、`cookie-encryption.key`、容器内密钥路径和容器用户读取权限 |
| 原 Token 收到 401 | 核对数据目录、授权开关、签名路径、原始查询串和两端时钟 |
| 搜索成功，播放返回 503 | 检查已启用账号的会员资格和目标音质。可以使用后台复验或导入已有的会员状态 |
| 汽水播放或响度服务失效 | 核对保存的服务地址和兼容容器的网络及 DNS |

## 验收边界

- Go 后台使用同源 HttpOnly 会话和 CSRF。原 Node 控制台的 X-Auth-Username /
  X-Auth-Token 会话契约与原版登录页仍由兼容部署提供。Go 扫码由后台验证并保存凭据。
- 独立版把 Qishui 音频缓存存入数据目录下的 go-qishui-audio-cache。缓存限制 512 MiB，
  过期时间六小时，Node 旧缓存可在兼容部署继续使用。
- 已验证真实上游的四平台搜索、二维码生成与等待状态。扫码确认、会员取链、QQ 续期
  和汽水二次验证已通过 mock 对照。生产环境仍需用部署方的真实账号与签名服务验收。
