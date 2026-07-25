# ClawFiles

ClawFiles 是一个面向手机使用的单目录文件投递器。它把服务器上的固定目录绑定到容器，提供文件浏览、分块上传、最近上传、基础预览，以及一键复制服务器绝对路径。

项目使用 Go 标准库实现后端和 HTTP 服务，前端资源直接嵌入二进制。运行时不需要 Node.js、数据库或额外文件服务。

## 当前功能

### 文件

- 浏览映射目录及子目录
- 新建文件夹
- 图片、视频、音频、PDF、文本、Markdown、JSON、CSV/TSV 预览
- DOCX 正文、XLSX 表格、PPTX 文字和 ZIP 文件列表的本地轻量预览
- 默认只预览不超过 20 MiB 的文件，超出后直接提示下载，避免占用手机内存
- 视频和大文件响应支持 HTTP Range
- 下载文件
- 复选框多选文件和文件夹
- 多选内容流式打包为 ZIP 下载
- 单个项目重命名，单个或多个项目移动到其他目录
- 勾选单个 ZIP 后可直接解压到当前目录的同名文件夹
- 批量删除并在操作前二次确认
- 一键复制服务器绝对路径
- 自动隐藏 ClawFiles 自己的元数据目录
- 不跟随目录中的符号链接

### 上传

- 多文件上传队列
- 每个文件独立显示进度和速度
- 桌面端最多使用 2 MiB 分块；iPhone/iPad 使用 128 KiB 内存分块，避开 PWA 文件流卡住
- 桌面端同时上传两个文件；iPhone/iPad 串行上传以降低 WebKit 连接压力
- 暂停、继续、取消和失败重试
- iPhone/iPad 切到后台时自动暂停，返回前台后从服务端已确认进度继续
- 单个分块 20 秒没有进度时自动中止并续传
- 局域网 HTTPS 地址优先，失败后自动切换 STUN，再回到当前 FRP 域名
- 刷新页面后，重新选择同一个文件可以从服务器保存的偏移量继续
- 同名文件不会覆盖，自动命名为 `文件名 (1).ext`
- 完成前保存在隐藏目录，完成后再原子链接到目标目录

### 最近

- 服务端持久化最近成功上传记录
- 文件被外部删除后会自动从返回结果中过滤
- 支持预览、下载和复制路径

### PWA

- 可安装到手机桌面
- 支持深色与浅色主题
- 手机和电脑顶部自动显示当前传输通道：局域网在线、STUN 通道在线或中转在线
- iPhone/iPad 的常规文件可通过系统分享菜单直接“存储到文件”，避免主屏 PWA 跳入 Quick Look
- 缓存应用外壳
- API、文件内容和预览不会进入 Service Worker 缓存

## 使用 Docker Compose

复制环境变量示例：

```bash
cp .env.example .env
```

编辑 `.env`：

```dotenv
CLAWFILES_IMAGE=ghcr.io/edwinzzzs2/openclaw_files:latest
HOST_STORAGE_PATH=/home/xixili/.openclaw/workspace/xixili/tmpo
APP_PASSWORD=设置一个长密码
HTTP_PORT=3661
PUID=1000
PGID=1000
COOKIE_SECURE=false
LAN_TRANSFER_ORIGIN=
STUN_TRANSFER_DOMAIN=
STUN_WEBHOOK_TOKEN=
TRANSFER_SIGNING_KEY=
MAX_PREVIEW_SIZE=20971520
```

确认 `PUID` 和 `PGID` 对 `HOST_STORAGE_PATH` 具有读写权限，然后启动：

```bash
docker compose up -d
```

打开：

```text
http://服务器地址:3661
```

这里的目录映射为：

```text
服务器 /home/xixili/.openclaw/workspace/xixili/tmpo  ->  容器 /data
```

页面复制的路径使用 `HOST_PATH_PREFIX`，Compose 已将它设置成 `HOST_STORAGE_PATH`。因此容器中的：

```text
/data/tasks/video.mp4
```

页面会复制为：

```text
/home/xixili/.openclaw/workspace/xixili/tmpo/tasks/video.mp4
```

`MAX_PREVIEW_SIZE` 使用字节数，默认 `20971520`（20 MiB）。Office 文件采用服务器本地的轻量解析，只读取可显示的文字、单元格和幻灯片文字，不会把文件发送给第三方预览服务，也不会还原复杂排版。

## HTTPS 与 PWA

除本机开发环境外，PWA 安装和安全 Cookie 都需要 HTTPS。建议在 ClawFiles 前使用 Caddy 或 Nginx。

启用 HTTPS 后设置：

```dotenv
COOKIE_SECURE=true
```

Nginx 示例：

```nginx
server {
    listen 443 ssl;
    server_name files.example.com;

    client_max_body_size 16m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_request_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;

        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

网页端最大上传分块为 2 MiB，因此反向代理只需要允许略大于单个分块的请求，而不需要允许单个 100 GB 请求。iPhone/iPad 会自动改用 128 KiB 分块。

## 局域网与 STUN 高速上传下载

### 核心思想：页面入口与传输入口分离

PWA 始终从稳定的 FRP 域名打开，登录、浏览目录、创建上传任务等小请求也始终走这个稳定入口。真正占用带宽的上传分块、单文件下载和批量 ZIP 下载，才根据当前网络切换到更快的地址。页面不会跳转，也不需要把 PWA 分别安装两次。

一套完整部署包含四个逻辑地址：

| 地址 | 示例 | 用途 |
|---|---|---|
| 稳定页面入口 | `https://clawfiles.dolast.top` | FRP 或公网反向代理，负责始终能打开 PWA |
| 局域网传输入口 | `https://clawfiles-lan.dolast.top:10010` | 家中 Wi-Fi 下直连 Lucky，速度最高且不消耗公网带宽 |
| STUN 传输入口 | `https://stun.example.com:动态端口` | 外网环境下使用家庭宽带 STUN 映射的动态端口 |
| ClawFiles 后端 | `http://192.168.31.73:3661` | Docker 暴露的纯 HTTP 服务，只供反向代理访问 |

局域网入口必须使用域名和有效 HTTPS 证书，不能让 HTTPS PWA 直接请求 `http://192.168.x.x`。否则会同时遇到浏览器混合内容限制、证书主机名不匹配和 iPhone PWA 本地网络限制。

### 自动选择流程

所有大流量操作统一按照以下优先级选择通道：

```text
LAN_TRANSFER_ORIGIN → STUN 动态地址 → 当前 FRP 域名
```

一次上传的完整流程如下：

1. PWA 先通过稳定入口创建上传任务，服务器返回任务 ID 和短期传输令牌。
2. PWA 请求 `/api/transfer`，获得固定局域网地址和当前 STUN 动态地址。
3. 客户端用短超时探测局域网上传端点；探测成功后，后续分块直接发送到局域网入口。
4. 局域网不可用时改用 STUN；STUN 也不可用时回到当前 FRP 域名。
5. 某条通道在上传过程中断开时，客户端重新读取服务端已确认偏移量，再从未完成位置切换通道续传，不会从零开始。

下载使用相同优先级。服务端先生成带短期签名的三个候选 URL，再由 PWA Service Worker 依次尝试局域网、STUN 和 FRP。API、文件内容和传输令牌不会进入 Service Worker 缓存。

页面顶部状态按同样的优先级自动探测：

- `局域网在线`：当前优先使用固定局域网 HTTPS 入口。
- `STUN 通道在线`：局域网不可用，当前使用动态映射端口。
- `中转在线`：局域网和 STUN 都不可用，当前使用稳定 FRP。

状态探测只是预判。真正开始上传或下载后，页面会以该次传输实际使用的通道更新状态。

### 为什么跨域传输不共享登录 Cookie

稳定域名、局域网域名和 STUN 域名可能完全不同，浏览器不会在这些域名之间共享登录 Cookie。ClawFiles 因此为每个上传任务、文件下载和批量下载生成有有效期的签名令牌：

- 局域网和 STUN 入口只接受正确签名的传输请求。
- 令牌绑定具体上传任务或文件路径，不能改成任意路径。
- CORS 只开放传输所需的 `GET`、`HEAD`、`PATCH` 和相关请求头。
- `TRANSFER_SIGNING_KEY` 只保存在服务器环境变量中，不会发送给浏览器。

`STUN_TRANSFER_DOMAIN` 可以与 PWA 当前域名完全不同，不需要配置 `PUBLIC_ORIGIN`。

### 第一步：配置稳定页面入口

使用 FRP、Lucky、Caddy 或 Nginx 提供一个稳定 HTTPS 域名，例如：

```text
https://clawfiles.dolast.top → http://192.168.31.73:3661
```

PWA 始终从这里打开和安装。即使局域网或 STUN 暂时不可用，也不会影响进入文件页面和管理已有文件。

### 第二步：配置局域网 HTTPS 入口

为局域网单独准备一个域名，并把 A 记录或局域网分流 DNS 指向运行 Lucky 的设备：

```text
clawfiles-lan.dolast.top → 192.168.31.57
Lucky HTTPS :10010 → http://192.168.31.73:3661
```

Lucky 中需要：

1. 新建 HTTPS 反向代理规则并监听 `10010`。
2. 域名填写 `clawfiles-lan.dolast.top`。
3. 为该域名配置有效 TLS 证书。
4. 后端地址填写 `http://192.168.31.73:3661`。
5. 不要开启会把请求再次重定向到公网域名的规则。

验证时直接访问：

```text
https://clawfiles-lan.dolast.top:10010/api/health
```

应快速返回：

```json
{"status":"ok"}
```

### 第三步：配置 OpenClash 和手机网络

如果 OpenClash 使用 Fake-IP 模式，仅添加 `DIRECT` 规则还不够。必须同时避免局域网域名被解析成 `198.18.0.0/16` 的 Fake-IP。

在自定义规则的 `rules:` 下加入：

```yaml
rules:
  - DOMAIN,clawfiles-lan.dolast.top,DIRECT
```

在 DNS 设置的自定义 Fake-IP-Filter 中加入：

```text
clawfiles-lan.dolast.top
```

Fake-IP-Filter 使用黑名单模式时，匹配到的域名应返回真实地址 `192.168.31.57`。修改后要保存、应用并重启 OpenClash，同时清理 Fake-IP/DNS 缓存，再让手机重新连接 Wi-Fi。

`LAN_TRANSFER_ORIGIN` 是 Docker Compose 环境变量，不能写进 OpenClash 的 `rules:` 编辑框。

iPhone 会优先尝试 IPv6。如果局域网没有完整可用的 IPv6 DNS、路由、Lucky 监听和防火墙配置，可能先等待 IPv6 超时再回退 IPv4，表现为首次检测或上传卡住。只使用 IPv4 时，建议在 OpenClash 中关闭“IPv6 代理”和“IPv6 DNS 解析”；若要保留 IPv6，则必须保证局域网域名的 AAAA 记录能直达 Lucky。

手机端还应确认：

- 当前连接的是同一个家庭 Wi-Fi。
- Wi-Fi 的 DNS 配置为“自动”，没有额外的加密 DNS 或 VPN 接管该域名。
- 如果系统询问本地网络访问权限，应选择允许。
- 修改 DNS 后完全关闭 PWA，再重新打开。

### 第四步：配置 Compose

Compose 中配置局域网入口和 STUN 签名参数：

```yaml
environment:
  COOKIE_SECURE: "true"
  LAN_TRANSFER_ORIGIN: "https://clawfiles-lan.dolast.top:10010"
  STUN_TRANSFER_DOMAIN: "stun.example.com"
  STUN_WEBHOOK_TOKEN: "至少24位的随机Webhook令牌"
  TRANSFER_SIGNING_KEY: "至少32位的随机签名密钥"
```

`LAN_TRANSFER_ORIGIN` 必须填写完整的 HTTPS Origin，可以包含固定端口，但不能包含路径、查询参数或片段。末尾 `/` 会被自动移除。局域网 DNS 应把该域名解析到运行 HTTPS 反向代理的设备，而不是直接解析到 ClawFiles 后端。例如：

```text
clawfiles-lan.dolast.top → 192.168.31.57
Lucky HTTPS :10010 → http://192.168.31.73:3661
```

局域网入口使用与 STUN 相同的短期签名令牌，因此配置 `LAN_TRANSFER_ORIGIN` 时仍需同时配置下面三个 STUN/签名变量。

`STUN_TRANSFER_DOMAIN` 只填写域名，不要带 `https://`、路径或端口。三个 STUN 变量必须一起填写；全部留空则关闭高速通道。可以用下面的命令生成随机值：

```bash
openssl rand -hex 24
openssl rand -hex 32
```

修改后更新容器：

```bash
docker compose pull
docker compose up -d
```

### 第五步：配置 STUN Webhook

端口变化时，请求：

```http
POST /api/webhooks/stun
Authorization: Bearer <STUN_WEBHOOK_TOKEN>
Content-Type: application/json
```

请求体：

```json
{
  "event": "stun_port_changed",
  "ip": "112.86.208.141",
  "port": 29717,
  "previous_port": 28566,
  "domains": ["app.example.com"],
  "ids": [],
  "updated_at": "2026-07-24T13:37:00.000Z"
}
```

Webhook 配置页面进行连通性测试时可以发送 `event: "test"`。测试数据中的 IP 和端口也会作为真实 STUN 地址立即保存；`updated_at` 可以是测试工具生成的说明文字，服务端会改用收到请求的时间：

```json
{
  "event": "test",
  "ip": "112.86.208.141",
  "port": 29717,
  "previous_port": 29717,
  "domains": [],
  "ids": [],
  "updated_at": "测试发送时间"
}
```

令牌、IP 和端口校验通过并保存成功后，ClawFiles 返回 HTTP 200：

```json
{
  "ok": true,
  "event": "test",
  "message": "Webhook 测试成功，STUN 地址已更新",
  "accepted": true,
  "stateUpdated": true,
  "ip": "112.86.208.141",
  "port": 29717,
  "endpoint": "https://clawfiles.dolast.top:29717",
  "updatedAt": "2026-07-25T01:30:00Z",
  "receivedAt": "2026-07-25T01:30:00Z"
}
```

所有错误统一返回：

```json
{"error": "具体错误原因"}
```

Token 错误返回 HTTP 401；JSON、事件、IP、端口或正式端口变更事件的时间错误返回 HTTP 400；STUN 未配置返回 HTTP 404；保存状态失败返回 HTTP 500。

ClawFiles 固定使用 YAML 中的 `STUN_TRANSFER_DOMAIN`，不会信任 Webhook 里的 `domains`。状态保存在 `/data/.clawfiles/stun.json`。正式的 `stun_port_changed` 通知若时间更早或重复会被安全忽略；`test` 通知按你的需求始终用收到请求的时间覆盖当前状态。

STUN 外部端口必须先进入 Lucky、Caddy 或 Nginx 的 HTTPS 监听端口，并由它使用 `STUN_TRANSFER_DOMAIN` 的有效证书终止 TLS，再反向代理到 ClawFiles 的 HTTP `:8080`。不要把公网 STUN 端口直接转发到 ClawFiles 的纯 HTTP 端口，否则浏览器无法通过 HTTPS 上传。

上传任务使用独立的短期签名令牌，不依赖不同域名之间共享 Cookie；下载通过 PWA 的 Service Worker 依次尝试局域网、STUN 和 FRP。

### 验证与排障

建议按从底层到页面的顺序验证：

```bash
# 1. 域名应返回 Lucky 的局域网地址，而不是 198.18.x.x
nslookup clawfiles-lan.dolast.top

# 2. HTTPS、证书和 Lucky 反向代理应返回 200
curl -i https://clawfiles-lan.dolast.top:10010/api/health

# 3. 查看容器是否正常
docker compose ps
docker compose logs --tail=100 clawfiles
```

常见现象：

| 现象 | 常见原因 | 处理方式 |
|---|---|---|
| DNS 返回 `198.18.x.x` | OpenClash Fake-IP-Filter 未生效或仍有缓存 | 加入精确域名，重启 OpenClash 并清理 DNS/Fake-IP 缓存 |
| 强制连接 `192.168.31.57` 正常，使用域名 TLS 失败 | Fake-IP、代理规则或 IPv6 抢先连接错误 | 检查 `DIRECT`、Fake-IP-Filter；不使用 IPv6 时关闭 IPv6 DNS/代理 |
| 电脑显示局域网，手机显示 STUN | 手机使用了不同 DNS、VPN、加密 DNS或 IPv6 路径 | 关闭相关接管，重连 Wi-Fi，完全关闭并重开 PWA |
| `/api/health` 正常，但上传仍切换到 STUN | 局域网上传端点的 `HEAD/PATCH` 被代理或 WAF 拦截 | Lucky 放行 `GET/HEAD/PATCH/OPTIONS` 及传输请求头 |
| 局域网首次稍慢，后续很快 | TLS 冷启动或证书链首次加载 | 首次响应低于探测超时即可，无需处理 |
| 页面长时间显示旧样式或旧状态 | PWA 外壳仍是旧 Service Worker | 完全关闭 PWA 后重开，必要时删除桌面 PWA 再安装 |
| STUN Webhook 返回 400 | JSON、事件、端口或时间字段校验失败 | 对照下方请求格式，并检查服务端日志 |

最终正常状态应满足：

1. 手机访问局域网 `/api/health` 能快速返回 200。
2. 家庭 Wi-Fi 下页面顶部显示“局域网在线”。
3. 关闭 Wi-Fi 后自动变为“STUN 通道在线”或“中转在线”。
4. 切换网络不会让上传从零开始，重新选择同一文件可以按服务端偏移量续传。

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---:|---|
| `LISTEN_ADDR` | `:8080` | HTTP 监听地址 |
| `STORAGE_ROOT` | `/data` | 容器内文件根目录 |
| `HOST_PATH_PREFIX` | 与 `STORAGE_ROOT` 相同 | 复制给 OpenClaw 的服务器路径前缀 |
| `APP_PASSWORD` | 空 | 登录密码。为空时关闭鉴权，不建议公网使用 |
| `COOKIE_SECURE` | `false` | HTTPS 部署时设置为 `true` |
| `MAX_UPLOAD_SIZE` | `107374182400` | 单文件上限，单位为字节，默认 100 GiB |
| `MAX_PREVIEW_SIZE` | `20971520` | 在线预览文件上限，单位为字节，默认 20 MiB |
| `UPLOAD_CHUNK_SIZE` | `2097152` | 服务端允许的分块上限，单位为字节，允许 1-64 MiB；网页端最多使用 2 MiB，iPhone/iPad 自动使用 128 KiB |
| `LAN_TRANSFER_ORIGIN` | 空 | 固定局域网 HTTPS Origin，可包含端口，例如 `https://clawfiles-lan.dolast.top:10010` |
| `STUN_TRANSFER_DOMAIN` | 空 | STUN 高速通道域名，只填写主机名 |
| `STUN_WEBHOOK_TOKEN` | 空 | 接收端口变化通知的 Bearer 令牌，至少 24 位 |
| `TRANSFER_SIGNING_KEY` | 空 | 跨域上传和下载令牌的签名密钥，至少 32 位 |

ClawFiles 会在映射目录下创建：

```text
.clawfiles/
  recent.json
  stun.json
  uploads/
```

其中 `uploads` 保存未完成上传的分块和偏移元数据。超过七天的未完成任务会在服务启动时清理。

## GitHub Container Registry

工作流位于 `.github/workflows/container.yml`：

- 推送到 `main` 时发布 `latest`
- 推送 `v1.2.3` 标签时发布对应版本
- 同时构建 `linux/amd64` 和 `linux/arm64`
- Pull Request 只构建，不推送
- 生成 SBOM、Provenance 和镜像证明

镜像地址：

```text
ghcr.io/<owner>/<repository>:latest
```

## 本地运行

```bash
set STORAGE_ROOT=./data
set HOST_PATH_PREFIX=D:\server\openclaw-files
set APP_PASSWORD=change-me
go run ./cmd/clawfiles
```

Linux 或 macOS：

```bash
STORAGE_ROOT=./data \
HOST_PATH_PREFIX=/srv/openclaw/files \
APP_PASSWORD=change-me \
go run ./cmd/clawfiles
```

## 安全边界

- 所有用户路径都在服务端重新规范化并验证根目录边界
- 目录列表和内容接口拒绝符号链接
- 文件名禁止路径分隔符和控制字符
- 修改请求要求同源 Cookie及自定义请求头
- 登录 Cookie 使用 HttpOnly 和 SameSite Strict
- SVG 与 HTML 以纯文本方式预览
- 容器默认无额外 Linux Capability，并启用 `no-new-privileges`

ClawFiles 只应挂载需要通过网页访问的目录，不要把整个服务器根目录或用户主目录映射给容器。
