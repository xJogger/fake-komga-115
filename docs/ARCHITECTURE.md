# Architecture

## 1. 总体数据与请求流（v2）

```text
浏览器（内置书架 / 内置 Suwayomi 阅读器 / /admin） / Mihon / Koharia
       │  Session Cookie / HTTP Basic Auth / X-API-Key
       ▼
internal/httpserver (authMiddleware + SPA + /reader + /admin/api + /api/v1 + /api/v2)
       ├── SQLite 主库 (鉴权、书库、进度、元数据) ── internal/database
       ├── Bangumi 双引擎刮削 + 离线索引库 ───────── internal/bangumi
       ├── 多源递归扫描器 ────────────────────────── internal/scanner
       ├── 统一归档读取器 (ZIP / RAR) ────────────── internal/archive
       │     ├── 115 Open API (downurl + Range) ──── internal/oneonefive + internal/cache
       │     ├── WebDAV (HTTP Range + 分块缓存) ───── internal/webdav + internal/cache
       │     └── Local FS (*os.File 原生直读) ────── internal/localfs (跳过 Range 缓存)
       ├── 系列封面与 Bangumi 懒加载封面缓存 ─────── internal/thumbnail
       └── 单部/系列归档维护任务 ─────────────────── internal/maintenance
```

核心原则保持不变：**启动和扫描阶段绝不下载或打开漫画压缩包内容**；只有用户打开某一卷的页表或翻到具体某页时，才按需读取对应字节区间。

## 2. 鉴权与会话安全

1. **状态判定**：服务端通过 `auth_admin` 表是否存在管理员记录判定是否已完成初始化（`HasAdmin`）。
2. **首次安装与升级平滑拦截**：
   - 未初始化时，`GET /health` 保持公开；浏览器页面统一 `307` 跳转至 `/setup`，API 请求统一返回 `401 SETUP_REQUIRED`。
   - 用户在 `/setup`（或 Komga 标准 `POST /api/v1/claim`）自定义用户名与密码后，密码经 `argon2id`（`time=3, memory=64MiB, threads=4`）哈希落库，此后 `/setup` 与 `/api/v1/claim` 永久关闭。
3. **三种认证凭证**：
   - **Session Cookie**：`fk115_session` 与兼容 Cookie `KOMGA-SESSION`（`HttpOnly`, `SameSite=Lax`），登录态永久有效（服务端会话永不过期，配合中间件静默滑动续期突破浏览器单次 400 天 Cookie 上限）；数据库仅存 `SHA-256(token)`。
   - **API Key**：`X-API-Key` 请求头，支持在后台创建多个命名密钥、查看前缀与最近使用时间并随时吊销；数据库仅存 `SHA-256(key)`。
   - **HTTP Basic Auth**：完整兼容 Mihon Komga 扩展与 Koharia。内存维护短时（5 分钟 TTL）凭据校验摘要缓存，避免高并发图片流重复触发 `argon2id` CPU 开销。当非浏览器 XHR 请求未认证时返回标准 `WWW-Authenticate: Basic` 头以触发 Mihon OkHttp Authenticator；内置前端请求附带 `X-FK115-UI: 1` 头以抑制浏览器原生弹窗。

## 3. 多存储源（115 / 本地目录 / WebDAV）与兼容升级

- `libraries` 表通过 `provider_type`（`115` | `local` | `webdav`）、`webdav_account_id`、`root_path` 区分来源：
  - **115 网盘（`115`）**：完全保留 v1 的 `id.Library(rootCID)`、`id.Series(libraryID, dirCID)`、`id.Book(libraryID, fileID)` 公式，现有几万系列与阅读进度、归档索引升级后 100% 原地保留。
  - **本地目录（`local`）**：`root_cid` 设为 `"local:" + cleanRootPath` 以满足唯一约束；扫描使用 `os.ReadDir`；读取归档时直接使用 `*os.File` 实现 `io.ReaderAt`，**完全绕过 Range 分块磁盘缓存**，避免本机磁盘双份占用。
  - **WebDAV（`webdav`）**：支持在 `webdav_accounts` 表配置多个 WebDAV 源（支持自定义限速与局域网自签名证书 `insecureTls`）；扫描通过 `PROPFIND Depth: 1` 递归列出目录；读取归档时通过 HTTP `Range: bytes=start-end` 复用 `RemoteReaderAt` 与 Range 分块缓存。

## 4. Bangumi (`bgm.tv`) 双引擎元数据刮削与封面懒缓存

针对数万部命名不规范漫画目录的自动刮削与性能需求，`internal/bangumi` 采用双引擎架构，且全程强制 `nsfw=false`：

1. **多规则漫画目录名清洗器（`parser.go` + `cjk.go`）**：
   - 内置繁简与日文新字体归一映射表；
   - 自动解析 `[书名][作者][出版社][卷数]`、`[作者] 书名`、末尾卷话后缀（`Vol.01-12`、`全18卷`、`完结`）、版本后缀（`完全版`、`爱藏版`、`新装版`、`电子版`、`扫图`）及常见出版社标记，生成候选搜索词与作者提示。
2. **引擎一：官方 `bangumi/Archive` 本地离线 SQLite 索引（批量主力）**：
   - 后台一键从 `bangumi/Archive` 拉取最新公开快照并校验 SHA256，流式解析 `subject.jsonlines` 与 `subject-relations.jsonlines`：仅保留 `type=1`（书籍）且 `nsfw=false` 的条目，识别并剔除从属于系列主条目的单行本分卷（`relation_type=1003`），解析 MediaWiki Infobox 提取作者/作画/原作/出版社/连载状态/别名，写入独立 `data/bangumi/bangumi-archive.db`。
   - 几万部漫画批量刮削全程在本地完成，无外部 API 频率限制。
3. **引擎二：实时 `api.bgm.tv/v0` 客户端（单部搜索与兜底）**：
   - 在作品详情页支持按关键词或直接输入 Bangumi Subject ID / URL 实时检索候选并一键绑定。
4. **显示、锁定、专用代理与按需封面缓存**：
   - 支持在后台配置独立的 Bangumi 网络代理（`http://`、`https://`、`socks5://`、`socks5h://`），统一加速 GitHub 离线快照下载、`api.bgm.tv` 与 `lain.bgm.tv` 封面抓取（含弹窗候选封面服务端代理预览），绝不影响 115 与 WebDAV 流量。
   - 刮削命中后存入 `series_metadata`：展示优先使用中文译名（`title`），同时保留原始目录名 `series.name` 可搜索、详情页可见；支持 Komga 标准 `*Lock` 字段锁。
   - 封面支持 `auto`（默认优先 Bangumi 官方封面，回退首卷首页）、`bangumi`、`book` 三档切换。Bangumi 封面在首次请求 `/api/v1/series/{id}/thumbnail` 时通过 `singleflight` 按需拉取并落盘缓存至 `data/thumbnails/bangumi/{subjectId}.jpg`，批量刮削数万条目时零图片网络风暴。

## 5. 内置书架、管理后台与网页阅读器

- **内置书架与后台 SPA（`internal/httpserver/static/ui/`）**：
  - 零外部运行时依赖，覆盖 `/setup`、`/login`、`/`（继续阅读、最近更新、最新入库）、`/libraries`（多维筛选与排序）、`/series/{id}`（阅读入口、Bangumi 匹配弹窗、元数据编辑弹窗、Mihon 维护统计抽屉、卷列表）、`/book/{id}` 及 `/admin`（概览、115/WebDAV/本地存储源、书库目录选择器、Bangumi 刮削中心、后台任务、缓存与安全设置）。
  - 任务状态采用自适应轮询：仅当存在 `queued`/`running` 任务且页面可见（`!document.hidden`）时轮询，空闲状态下零后台请求刷屏。
- **内置网页阅读器（`/reader/*`，`web/reader/` + `scripts/build-reader.sh`）**：
  - 基于上游 `Suwayomi/Suwayomi-WebUI`（`MPL-2.0`）的独立阅读器构建，通过 `KomgaHostBridge` 将 Apollo 操作内存桥接至同源 `/api/v1/*` Komga REST 接口，无 GraphQL / WebSocket 依赖。
  - 支持右翻双页拼合（宽图跨页自动独占）、单页、条漫长图滚动、键盘快捷键、触控热区、阅读进度双向防抖同步（`PATCH /api/v1/books/{id}/read-progress` + `pagehide` keepalive 刷新）以及跨卷无缝切换。

## 6. 纯 Go 构建与移动端就绪

服务端保持 `100% CGO_ENABLED=0`（`modernc.org/sqlite`、`rardecode/v2`、`golang.org/x/crypto/argon2`）。`internal/app.App` 提供独立于 CLI 的 `Start()`、`ListenAddr()`、`Shutdown()` 与 `Close()` 生命周期接口，后续可直接由 `gomobile bind` 封装为 Android 本地服务配合 WebView 壳打包原生 APK。
