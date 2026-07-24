# ClawFiles

ClawFiles 是一个面向手机使用的单目录文件投递器。它把服务器上的固定目录绑定到容器，提供文件浏览、分块上传、最近上传、基础预览，以及一键复制服务器绝对路径。

项目使用 Go 标准库实现后端和 HTTP 服务，前端资源直接嵌入二进制。运行时不需要 Node.js、数据库或额外文件服务。

## 当前功能

### 文件

- 浏览映射目录及子目录
- 新建文件夹
- 图片、视频、音频、PDF、文本和常见代码文件预览
- 视频和大文件响应支持 HTTP Range
- 下载文件
- 一键复制服务器绝对路径
- 自动隐藏 ClawFiles 自己的元数据目录
- 不跟随目录中的符号链接

### 上传

- 多文件上传队列
- 每个文件独立显示进度和速度
- 默认使用 8 MiB 分块
- 同时上传两个文件
- 暂停、继续、取消和失败重试
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

默认上传分块为 8 MiB，因此反向代理只需要允许略大于单个分块的请求，而不需要允许单个 100 GB 请求。

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---:|---|
| `LISTEN_ADDR` | `:8080` | HTTP 监听地址 |
| `STORAGE_ROOT` | `/data` | 容器内文件根目录 |
| `HOST_PATH_PREFIX` | 与 `STORAGE_ROOT` 相同 | 复制给 OpenClaw 的服务器路径前缀 |
| `APP_PASSWORD` | 空 | 登录密码。为空时关闭鉴权，不建议公网使用 |
| `COOKIE_SECURE` | `false` | HTTPS 部署时设置为 `true` |
| `MAX_UPLOAD_SIZE` | `107374182400` | 单文件上限，单位为字节，默认 100 GiB |
| `UPLOAD_CHUNK_SIZE` | `8388608` | 分块大小，单位为字节，允许 1-64 MiB |

ClawFiles 会在映射目录下创建：

```text
.clawfiles/
  recent.json
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
