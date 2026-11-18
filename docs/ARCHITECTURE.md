# Architecture

## 1. 总体数据与请求流（v2）

```text
浏览器（内置书架 / 内置简体中文阅读器 / /admin） · Mihon / Koharia · AI Agent
       │  Session Cookie / HTTP Basic Auth / Authorization: Bearer / X-API-Key
       ▼
internal/httpserver (authMiddleware + SPA + /reader + /admin/api + /api/agent/v1 + /api/v1 + /api/v2)
       ├── SQLite 主库 (鉴权、书库、隐藏与 NSFW 隔离、进度、元数据) ── internal/database
       ├── Bangumi 双引擎刮削 + 本地离线索引库 + 独立代理 ──────────── internal/bangumi
       ├── 多源递归扫描器 (含 SMB 空挂载点保护) ────────────────────── internal/scanner
       ├── 统一归档读取器 (ZIP / RAR) ──────────────────────────────── internal/archive
       │     ├── 115 Open API (downurl + Range 分块缓存) ───────────── internal/oneonefive + internal/cache
       │     ├── WebDAV (令牌桶限速 + HTTP Range + 分块缓存) ───────── internal/webdav + internal/cache
       │     └── Local / SMB FS (*os.File 原生直读，跳过 Range 缓存) ─ internal/localfs
       ├── 系列封面与 Bangumi 懒加载封面缓存 ───────────────────────── internal/thumbnail
       └── 单部/系列归档维护任务 ───────────────────────────────────── internal/maintenance
```

核心原则保持不变：**启动和扫描阶段绝不下载或打开漫画压缩包内容**；只有用户打开某一卷的页表或翻到具体某页时，才按需读取对应字节区间。

## 2. 鉴权与会话安全

1. **状态判定**：服务端通过 `auth_admin` 表是否存在管理员记录判定是否已完成初始化（`HasAdmin`）。
2. **首次安装与升级平滑拦截**：
   - 未初始化时，`GET /health` 保持公开；浏览器页面统一 `307` 跳转至 `/setup`，API 请求统一返回 `401 SETUP_REQUIRED`。
   - 用户在 `/setup`（或 Komga 标准 `POST /api/v1/claim`）自定义用户名与密码后，密码经 `argon2id`（`time=3, memory=64MiB, threads=4`）哈希落库，此后 `/setup` 与 `/api/v1/claim` 永久关闭。
3. **三种认证凭证**：
   - **Session Cookie**：`fk115_session` 与兼容 Cookie `KOMGA-SESSION`（`HttpOnly`, `SameSite=Lax`），登录态永久有效（服务端会话永不过期，配合中间件静默滑动续期突破浏览器单次 400 天 Cookie 上限）；数据库仅存 `SHA-256(token)`。
   - **API Key**：同时支持 `Authorization: Bearer <key>` 与 `X-API-Key: <key>` 请求头，支持在后台创建多个命名密钥、查看前缀与最近使用时间并随时吊销；数据库仅存 `SHA-256(key)`。
   - **HTTP Basic Auth**：完整兼容 Mihon Komga 扩展与 Koharia。内存维护短时（5 分钟 TTL）凭据校验摘要缓存，避免高并发图片流重复触发 `argon2id` CPU 开销。内置前端请求附带 `X-FK115-UI: 1` 头以抑制浏览器原生弹窗。

## 3. 多存储源（115 / 本地与 SMB 挂载目录 / WebDAV）与隐藏隔离

- `libraries` 表通过 `provider_type`（`115` | `local` | `webdav`）、`webdav_account_id`、`root_path`、`enabled` 区分来源与可见性：
  - **115 网盘（`115`）**：完全保留 v1 的 `id.Library(rootCID)`、`id.Series(libraryID, dirCID)`、`id.Book(libraryID, fileID)` 公式，现有几万系列与阅读进度、归档索引升级后 100% 原地保留。
  - **本地与 SMB 挂载目录（`local`）**：`root_cid` 设为 `"local:" + cleanRootPath`；支持 Linux CIFS/SMB 挂载点（如 `/share/manga`）与 Windows 映射盘符/UNC 路径。`localfs.ListDirectory` 优先复用 `DirEntry.Info()` 仅对符号链接/重解析点执行 `os.Stat` 与 `EvalSymlinks`，避免 SMB 元数据放大；`scanner.scanLocal` 在数据库已有书籍且根目录返回 `0` 项时自动拒绝提交扫描，防止 SMB 掉线导致误清空；读取归档时直接使用 `*os.File` 实现 `io.ReaderAt`，**完全绕过 Range 分块磁盘缓存**。
  - **WebDAV（`webdav`）**：支持在 `webdav_accounts` 表配置多个 WebDAV 源（兼容 CloudDrive2 `:19798/dav`、OpenList `:5244/dav`、群晖、Nextcloud，支持自定义限速与自签名证书 `insecureTls`）；令牌桶限速同时约束 `PROPFIND` 扫描与 `FetchRange` 分块读取。
  - **隐藏书库（`enabled = 0`）**：保留所有系列、单册、Bangumi 元数据、阅读进度与磁盘缓存；`SeriesByID` / `BookByID` / `SeriesPage` / `BooksPage` 统一要求 `libraries.enabled = 1`，隐藏瞬间自动调用 `cancelLibraryJobs` 中止运行中任务。

## 4. Bangumi (`bgm.tv`) 双引擎刮削与 NSFW 默认隐藏

1. **多规则漫画目录名清洗器（`parser.go` + `cjk.go`）**：
   - 扩充繁简、台湾异体字与日文新字体归一映射表；
   - 支持 `[标题][作者]` 与 `[作者][标题]` 双序多方括号提取、小写 `x` 合著切分、括号内外双标题提取、中英边界切分、扫图组噪音黑名单以及通用子目录（`台版`、`特别篇`、`单行本` 等）向父目录回溯并追加显示后缀（如 `水星领航员（台版）`）。
2. **引擎一：官方 `bangumi/Archive` 本地离线 SQLite 索引（批量主力）**：
   - 流式解析 `subject.jsonlines` 与 `subject-relations.jsonlines`，导入全量 `type=1` 书籍主条目（记录官方布尔 `is_nsfw`，剔除 `relation_type=1003` 单行本分卷），写入 `data/bangumi/bangumi-archive.db`。
   - 启动时若检测到本地 `terms_schema_version` 落后，自动在本地从 `bgm_subjects` 秒级重建 `bgm_terms` 词表，无需重新下载快照压缩包。
   - 批量刮削支持 `incremental`（仅新增）、`retry_unmatched`（重试未命中 + 新增）与 `force`（全量）三档模式。
3. **引擎二：实时 `api.bgm.tv/v0` 客户端与独立代理**：
   - 支持按关键词或 Subject ID 实时搜索绑定；独立代理配置统一作用于离线包下载、API 查询与封面抓取。
4. **NSFW 分级规则与默认隐藏（`show_nsfw`）**：
   - Bangumi 匹配结果**严格且仅**依据官方布尔字段 `nsfw == true` 标记 `series_metadata.is_nsfw = 1`（绝不因用户标签误伤 `nsfw: false` 的条目）；自动未命中条目则通过 `InferPathNSFW` 识别显式成人目录特征；支持 `nsfwLock` 手动覆盖锁定。
   - 全局设置 `show_nsfw` 默认 `"false"`：关闭时在前台书架、分类/出版社聚合、Komga 客户端与阅读器直链中过滤 `is_nsfw = 1` 系列（直链返回 `404`），而后台扫描、刮削与 `/api/agent/v1` 仍可完整管理。

## 5. 内置书架、简体中文阅读器与 Agent 接口

- **内置书架与后台 SPA（`internal/httpserver/static/ui/`）**：支持系统自动深浅色切换、移动端防横向溢出栅格、缓存页全 `MiB` 单位展示与输入、NSFW 开关与自适应空闲零轮询。
- **内置简体中文阅读器（`/reader/*`，`web/reader/` + `scripts/build-reader.sh`）**：基于上游 `Suwayomi/Suwayomi-WebUI`（`MPL-2.0`）构建，默认加载 `zh-Hans` 简体中文语言包并在首屏渲染前完成激活，通过 `KomgaHostBridge` 内存桥接至 `/api/v1/*` Komga REST 接口。
- **Agent 专用接口（`/api/agent/v1/*`，`internal/httpserver/agent.go`）**：提供 `/manifest`、`/overview`、书库 CRUD、紧凑分页 `/series`（支持 `nsfw=all|only|exclude`）与单次最多 200 条异构 `/series/batch`，详见 `docs/AGENT_API.md`。

## 6. 纯 Go 构建与移动端就绪

服务端保持 `100% CGO_ENABLED=0`（`modernc.org/sqlite`、`rardecode/v2`、`golang.org/x/crypto/argon2`）。`internal/app.App` 提供独立于 CLI 的 `Start()`、`ListenAddr()`、`Shutdown()` 与 `Close()` 生命周期接口，后续可直接由 `gomobile bind` 封装为 Android 本地服务配合 WebView 壳打包原生 APK。
