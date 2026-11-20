# AGENTS.md

本文档面向 AI 编程助手、书库管理 Agent 以及参与二次开发的贡献者。
- 若你是参与代码修改的 **AI 编程助手**，开始修改前请先阅读 `README.md`、`SECURITY.md`、`docs/ARCHITECTURE.md` 与 `docs/AGENT_API.md`。
- 若你是通过 HTTP 接口管理运行中书库的 **运维 / 整理 Agent**，请直接阅读 `docs/AGENT_API.md` 并请求 `GET /api/agent/v1/manifest`。

## 项目目标与核心约束

这是一个轻量、纯 Go（`CGO_ENABLED=0`）自包含的 Komga 兼容漫画服务端，内置响应式书架、简体中文双页网页阅读器、Bangumi 元数据刮削引擎与 Agent 专用管理接口，支持 115 Open API、服务器本地 / SMB 挂载目录和 WebDAV（CloudDrive2 / OpenList / NAS）三类存储源。核心约束：

1. **扫描只读目录结构 + 空挂载点保护**：扫描阶段只列出 115 / 本地（含 SMB）/ WebDAV 的目录和归档文件大小、时间元数据，绝不读取压缩包内部字节。对于已有书籍记录的本地/SMB 书库，若根目录扫描返回 `0` 项（如 SMB 断连未挂载），必须拒绝提交扫描以保护历史数据。
2. **严格按需流式读取**：115 与 WebDAV 仅在请求具体漫画归档索引或页面时通过 HTTP Range 分块读取（WebDAV 同时对 `PROPFIND` 与 `Range` 实施令牌桶限速）；本地与 SMB 挂载目录直接通过 `*os.File`（`io.ReaderAt`）随机读取，自动跳过 Range 分块磁盘缓存。
3. **单管理员强制鉴权**：未完成首次管理员设密前，除 `/health`、`/setup` 与初始 Claim 端点外拒绝一切业务请求；设密完成后永久关闭初始化入口。API 同时支持 Session Cookie、`HTTP Basic Auth`、`Authorization: Bearer <key>` 与 `X-API-Key: <key>`。
4. **隐藏书库与 NSFW 默认隐藏隔离**：
   - **隐藏书库（`libraries.enabled = 0`）**：保留全部数据库记录与缓存，但在前台书架、Komga 客户端、阅读器直链（统一返回 `404`）及扫描/刮削任务中完全隔离，隐藏瞬间自动取消该库进行中任务。
   - **NSFW 分级与默认隐藏（`show_nsfw` 默认 `"false"`）**：Bangumi 命中条目**仅**依据官方布尔字段 `nsfw == true` 标记 `is_nsfw`（严禁将 `nsfw == false` 但带用户标签的普通条目误判为 NSFW），未命中条目辅助识别显式成人路径词；`show_nsfw = "false"` 时在前台书架、客户端与阅读直链中自动隐藏，但后台扫描、Bangumi 刮削与 `/api/agent/v1/*` 仍可正常管理。
5. **保持 100% 纯 Go 无 CGO**：便于跨平台静态编译及后续 Android `gomobile` 原生封装。

## 开发与构建命令

```bash
bash scripts/build-reader.sh
node --check internal/httpserver/static/ui/app.js
gofmt -w cmd internal
go test -count=1 -race ./...
go vet ./...
CGO_ENABLED=0 go build -o /tmp/mangaalpha ./cmd/server
```

注意：`internal/httpserver/static/reader_dist/` 的编译产物已被 `.gitignore` 忽略（仅保留 `placeholder.txt` 使无前端产物的纯 Go 单测可直接编译）。正式打包二进制或镜像前必须先运行 `bash scripts/build-reader.sh`（或 `make build`）。

## 目录职责

- `cmd/server`：CLI 参数、信号处理和进程生命周期。
- `internal/app`：组装数据库、115、WebDAV、Bangumi、扫描器、缓存、归档和 HTTP 服务，暴露 `Start()` / `ListenAddr()` / `Shutdown()` / `Close()`。
- `internal/database`：SQLite schema、幂等升级迁移、鉴权哈希（`argon2id` + `SHA-256`）、多存储源、隐藏书库与 NSFW 可见性过滤、系列元数据与阅读进度查询。
- `internal/oneonefive`：115 Open SDK 封装、Token 自动刷新、限速、目录列表与 `downurl` 获取。
- `internal/localfs`：本地与 SMB 挂载目录校验、免多余 `stat` 高效遍历、系统垃圾文件过滤与目录浏览。
- `internal/webdav`：多账号 WebDAV `PROPFIND` 目录遍历、连接测试、全局请求限速（含 `FetchRange`）与 HTTP `Range` 读取。
- `internal/scanner`：多存储源统一递归扫描、SMB 空挂载点防清空保护、普通模式与 One-Shots 事务暂存转换。
- `internal/archive`：统一归档打开器（115/WebDAV 走 `RemoteReaderAt` + Range 缓存；本地/SMB 文件走原生 `*os.File`）及 ZIP/RAR 索引与单页解压。
- `internal/bangumi`：繁简/日文异体归一词表、双序 `[作者][标题]`/`[标题][作者]` 与子目录回溯清洗器、`bangumi/Archive` 本地离线 SQLite 索引库（支持本地词表自动升级）、`api.bgm.tv` 实时客户端、独立代理与三档批量刮削调度。
- `internal/cache`：Range block 与已解压页面的磁盘缓存与 LRU 淘汰。
- `internal/thumbnail`：封面服务（支持 `auto`/`bangumi`/`book` 优先级与 Bangumi 封面按需 `singleflight` 懒缓存）及批量首卷封面任务。
- `internal/maintenance`：Series / Book 手动索引与封面任务。
- `internal/httpserver`：鉴权中间件、内置书架与 `/admin` SPA（`static/ui/`）、内置阅读器静态托管（`static/reader_dist/`）、管理 API（`/admin/api`）、Agent 专用 API（`/api/agent/v1`）与 Komga v1/v2 兼容 API。
- `web/reader` + `scripts/build-reader.sh`：基于上游 `Suwayomi-WebUI`（MPL-2.0）的简体中文默认 `/reader/*` Komga REST 宿主适配层与确定性构建脚本。

## 安全与红线规则

- 绝不把真实 Token、WebDAV 密码、代理密码、Session Cookie、API Key、真实 `downurl` 或私人共享目录路径写入日志、代码或测试夹具。
- 绝不在本公开仓库中引入任何私有仓库域名、内网 IP 或内部业务标识。
- 已有 `115` 书库的 `Library` / `Series` / `Book` ID 生成公式不可更改，确保旧版数据库无缝原地升级。
- 前端任务进度严禁在空闲状态下无条件轮询刷屏；仅当存在 `queued`/`running` 任务且页面处于前台可见状态时才按需轮询。
