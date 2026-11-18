# AGENTS.md

本文档面向 AI 编程助手和参与二次开发的贡献者。开始修改前，先阅读
`README.md`、`SECURITY.md` 和 `docs/ARCHITECTURE.md`。

## 项目目标

这是一个轻量、纯 Go（`CGO_ENABLED=0`）自包含的 Komga 兼容漫画服务端，内置响应式书架、网页阅读器与 Bangumi 元数据刮削引擎，同时支持 115 Open API、服务器本地目录和 WebDAV 三类存储源。核心约束：

1. **扫描只读目录结构**：扫描阶段只列出 115 / 本地 / WebDAV 的目录和归档文件大小、时间元数据，绝不读取压缩包内部字节。
2. **严格按需流式读取**：115 与 WebDAV 仅在请求具体漫画归档索引或页面时通过 HTTP Range 分块读取；本地目录直接通过 `*os.File`（`io.ReaderAt`）随机读取，自动跳过 Range 分块磁盘缓存。
3. **单管理员强制鉴权**：未完成首次管理员设密前，除 `/health`、`/setup` 与初始 Claim 端点外拒绝一切业务请求；设密完成后永久关闭初始化入口。
4. **Bangumi 严格禁 NSFW**：离线索引库（`bangumi/Archive`）与实时搜索（`api.bgm.tv/v0`）均强制过滤 `nsfw=false`，不接收或暴露 NSFW 内容。
5. **保持 100% 纯 Go 无 CGO**：便于跨平台交叉编译及后续 Android `gomobile` 原生封装。

## 开发与构建命令

```bash
bash scripts/build-reader.sh
node --check internal/httpserver/static/ui/app.js
gofmt -w cmd internal
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build -o /tmp/fake-komga-115 ./cmd/server
```

注意：`internal/httpserver/static/reader_dist/` 的编译产物已被 `.gitignore` 忽略（仅保留 `placeholder.txt` 使无前端产物的纯 Go 单测可直接编译）。正式打包二进制或镜像前必须先运行 `bash scripts/build-reader.sh`（或 `make build`）。

## 目录职责

- `cmd/server`：CLI 参数、信号处理和进程生命周期。
- `internal/app`：组装数据库、115、WebDAV、Bangumi、扫描器、缓存、归档和 HTTP 服务，暴露 `Start()` / `ListenAddr()` / `Shutdown()` / `Close()`。
- `internal/database`：SQLite schema、幂等升级迁移、鉴权哈希（`argon2id` + `SHA-256`）、多存储源、系列元数据与阅读进度查询。
- `internal/oneonefive`：115 Open SDK 封装、Token 自动刷新、限速、目录列表与 `downurl` 获取。
- `internal/localfs`：本地目录校验、后台文件夹浏览与递归扫描辅助。
- `internal/webdav`：多账号 WebDAV `PROPFIND` 目录遍历、连接测试、限速与 HTTP `Range` 读取。
- `internal/scanner`：多存储源统一递归扫描、普通模式与 One-Shots 事务暂存转换。
- `internal/archive`：统一归档打开器（115/WebDAV 走 `RemoteReaderAt` + Range 缓存；本地文件走原生 `*os.File`）及 ZIP/RAR 索引与单页解压。
- `internal/bangumi`：繁简归一词表、漫画目录名智能清洗器、MediaWiki Infobox 解析、`bangumi/Archive` 本地离线 SQLite 索引库与 `api.bgm.tv` 实时客户端。
- `internal/cache`：Range block 与已解压页面的磁盘缓存与 LRU 淘汰。
- `internal/thumbnail`：封面服务（支持 `auto`/`bangumi`/`book` 优先级与 Bangumi 封面按需 `singleflight` 懒缓存）及批量首卷封面任务。
- `internal/maintenance`：Series / Book 手动索引与封面任务。
- `internal/httpserver`：鉴权中间件、内置书架与 `/admin` SPA（`static/ui/`）、内置阅读器静态托管（`static/reader_dist/`）、管理 API 与 Komga v1/v2 兼容 API。
- `web/reader` + `scripts/build-reader.sh`：基于上游 `Suwayomi-WebUI`（MPL-2.0）的独立 `/reader/*` Komga REST 宿主适配层与确定性构建脚本。

## 安全与红线规则

- 绝不把真实 Token、WebDAV 密码、Session Cookie、API Key、真实 `downurl` 或私人库路径写入日志、代码或测试夹具。
- 绝不在本公开仓库中引入任何私有仓库域名、内网 IP 或内部业务标识。
- 已有 `115` 书库的 `Library` / `Series` / `Book` ID 生成公式不可更改，确保旧版数据库无缝原地升级。
- 前端任务进度严禁在空闲状态下无条件轮询刷屏；仅当存在 `queued`/`running` 任务且页面处于前台可见状态时才按需轮询。
