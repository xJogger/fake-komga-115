# Architecture

## 1. 请求与数据流

```text
Mihon / browser
       │ Komga-compatible HTTP API
       ▼
internal/httpserver
       ├── SQLite metadata ────────────── internal/database
       ├── recursive scan ─────────────── internal/scanner
       ├── 115 Open API ───────────────── internal/oneonefive
       ├── remote ZIP/RAR reader ──────── internal/archive
       ├── range/page cache ───────────── internal/cache
       ├── cover service and jobs ─────── internal/thumbnail
       └── detail maintenance jobs ────── internal/maintenance
```

服务启动时只打开 SQLite、初始化组件并启动扫描调度器。漫画内容不会在启动或扫描
时下载。页面请求的读取路径为：

```text
Book metadata
  → cached or refreshed 115 downurl
  → HTTP Range
  → range block cache
  → ZIP/RAR entry parsing
  → optional page decompression cache
  → image response
```

## 2. 进程入口和组件组装

`cmd/server/main.go` 解析：

- `--host` / `FK115_HOST`
- `--port` / `FK115_PORT`
- `--data-dir` / `FK115_DATA_DIR`
- `--open-browser` / `FK115_OPEN_BROWSER`
- `--version`

`internal/app.New` 依次创建 Store、Cache Manager、115 Client、Scanner、Archive
Service、Thumbnail Service、Thumbnail Batch Manager、Maintenance Manager 和 HTTP
Server。关闭时先停止详情页维护任务、封面任务和扫描上下文，再关闭数据库。

Windows 未指定数据目录时使用 `%LOCALAPPDATA%\fake-komga-115\data`，并默认在
TCP 监听成功后打开本机管理页。Linux 和 macOS 仍默认使用 `./data` 且不主动打开
浏览器。Docker 镜像通过环境变量把数据目录设为 `/data`，并显式关闭自动打开浏览器；
容器内进程以非 root 用户运行。监听 socket 在 `App.Start` 中同步绑定，避免端口占用
时仍误开浏览器。

管理状态 API 会返回规范化后的绝对数据目录、版本和检测到的私有 IPv4 局域网
地址，供管理页显示 Mihon 配置值；常见容器虚拟网卡会被忽略。

## 3. SQLite 数据模型

主要表：

- `settings`：扫描、限速、Range block、缓存和预读设置。
- `provider_accounts`：单个 115 账号的 Access/Refresh Token。
- `libraries`：115 根目录和普通/One-Shots 模式。
- `scan_runs`：扫描队列、进度、结果和取消标记。
- `series`：Komga Series 元数据及 `seen_scan_id`。
- `books`：115 漫画归档文件元数据及 `seen_scan_id`。
- `scan_series_staging` / `scan_books_staging`：模式转换的临时完整快照。
- `zip_indexes`：ZIP 和 RAR 共用的页面索引 JSON、页数和最近一次成功建索引耗时。
- `series_thumbnails`：生成封面的文件信息、源 Book 版本和最近一次生成耗时。
- `thumbnail_runs`：手动封面任务的队列、进度、结果、错误摘要和取消标记。
- `book_read_progress`：Mihon/Komga Tracker 同步得到的 Book 级已读状态。
- `book_page_progress`：页面图片成功返回后推断出的最近加载页、最大加载页和页数。
- `book_download_stats`：实际从 115 downurl 成功读取 Range 的累计字节、耗时和次数。
- `maintenance_runs`：Series / Book 信息页手动封面和索引任务的持久化进度。
- `downurl_cache`：短期 downurl 和 User-Agent。
- `cache_entries`：Range/Page 磁盘缓存的大小与访问时间。

数据库启用 WAL、foreign keys 和 busy timeout。新字段必须同时更新 fresh schema 和
升级迁移。当前迁移通过 `PRAGMA table_info` 判断缺失列。

Komga Book DTO 的 `created` 和 `metadata.created` 优先使用 `books.file_created_at`
（115 文件创建时间），仅在旧数据缺失该字段时回退到本地数据库记录创建时间。

Series 的 `createdDate` 排序取其所有 Book 中最大的 `file_created_at`，而
`lastModifiedDate` 排序取最大的 `file_modified_at`。这样普通多卷 Series 和
One-Shots 使用相同语义，并且不会按扫描写入数据库的时间排序。

## 4. 稳定 ID

`internal/id` 使用 URL-safe Base64 编码：

- Library：根目录 CID
- 普通 Series：Library ID + 目录 CID
- One-Shot Series：Library ID + `file:<fileID>`
- Book：Library ID + 115 File ID

因此同名目录或文件仍由 115 ID 区分。不要改成仅根据显示名称生成 ID。

## 5. 扫描模型

扫描器只有一个 worker，同时最多执行一个扫描任务，但队列可以包含多个 Library。
115 API 调用由共享 rate limiter 限速。

### 5.1 普通模式

使用广度优先队列递归列出目录：

1. 每次 `ListDirectory` 得到子目录和文件元数据。
2. 子目录进入队列。
3. 当前目录有支持的漫画归档时，目录成为一个 Series。
4. 文件按自然顺序成为 Book。
5. 只有子目录而没有直接漫画文件的目录不生成 Series。

### 5.2 One-Shots

仍然递归遍历全部目录，但每个支持的归档文件创建一个独立 Series 和一个 Book：

- Series 名称：文件名去扩展名
- Book 名称：原始文件名
- Series ID：基于 File ID
- 同名文件允许存在

### 5.3 成功提交和模式转换

同模式重扫直接 upsert，并用当前 `seen_scan_id` 标记记录。只有扫描成功后才删除
未出现的旧 Book/Series。

模式转换不能直接覆盖旧结构。扫描先把目标结构写入 staging 表；全部目录成功后，
在单个 SQLite 事务中删除旧结构、导入 staging 数据并清空 staging。失败、取消或
服务重启只清理 staging，上一次完整结构仍可读取。

## 6. 115 客户端

`internal/oneonefive` 包装 `github.com/OpenListTeam/115-sdk-go`：

- 从 SQLite 加载 Token。
- SDK 刷新 Token 后回写 SQLite。
- 用 `golang.org/x/time/rate` 限制 API 请求速率。
- `GetFiles` 每页最多请求 1150 项并按文件名升序。
- 列表结果转换为内部 `File`，保留 ID、Parent ID、Size、Pick Code、SHA1 和时间。
- downurl 与请求使用的 User-Agent 必须配套。

任何调用方都不能记录 Token 或完整 downurl。

## 7. 远程 Range Reader

`archive.RemoteReaderAt` 为 Go ZIP reader 和 RAR 文件系统提供 `ReadAt`：

1. 按配置的 block size 对齐读取。
2. 缓存键包含 File ID、Size、SHA1、block size 和 block index。
3. 读取 downurl cache；快过期时重新请求。
4. 发出带 `Range` 和 `Accept-Encoding: identity` 的 GET。
5. 必须得到 `206 Partial Content` 和准确字节数。
6. 401/403/404/416 会使 downurl 失效并重试一次。
7. 返回 200 表示远端不支持 Range，映射为 `ErrRangeNotSupported`。

完整 downurl 只存在 SQLite 和内存中，不应出现在日志或 API 返回。
成功从远端读取的 Range block 会按 Book 累加到 `book_download_stats`。缓存命中、
等待同一个 inflight 下载结果和失败请求不计入真实下载速度。

## 8. ZIP 页面

ZIP 使用标准库 `archive/zip.NewReader` 读取 central directory：

- 忽略目录、`__MACOSX`、`.DS_Store` 和 `Thumbs.db`。
- 只把 JPEG、PNG、WebP、GIF、AVIF 作为页面。
- 页面按自然文件名排序。
- 只支持 Store 和 Deflate；加密或其他压缩方法返回不支持。
- Store 页面直接读取；Deflate 页面按需解压并进入 Page cache。
- 校验解压大小和 CRC32。

## 9. RAR 页面

RAR 使用 `rardecode/v2` 和一个只暴露单个远程 `archive.rar` 的 `fs.FS`：

- 建索引时使用较小的 RAR index block。
- 读取页面压缩数据时切换到普通 Range block。
- 页面索引保存 archive entry 的位置、名称和大小。
- 每次读页重新列出文件头并验证 entry 没有变化。
- 拒绝固实、加密、分卷、未知大小和超出字典上限的归档。
- 解压结果进入 Page cache。

## 10. 缓存

### Range cache

保存远程文件字节块。对 ZIP Store 页面也会复用。默认上限 10 GiB。

### Page cache

保存已解压的 ZIP Deflate 或 RAR 图片。默认上限 5 GiB。

### Archive index

保存在 SQLite，按 `BookVersion` 失效。`BookVersion` 包含 File ID、格式、Size、
SHA1 和修改时间。首次建立或强制重建成功时记录本次索引耗时；直接命中已有索引
不会更新耗时。

### Thumbnail

用户请求 Series 第一本 Book 的第一页后异步生成。输入像素数有限制，输出 JPEG
质量 75，最长边 300px。数据库记录源 Book 和版本，源变化后自动失效。
成功生成时记录最近一次封面生成耗时。阅读触发的自动封面只统计压缩写入阶段；
手动详情页任务和管理页批量任务会把读取第一页到写入封面的总耗时写回该字段。

管理页也可以按 Library 启动后台封面任务：

- `latest` 模式按 Series 中最大 `books.file_modified_at` 降序选择最新 N 个，默认
  N 为 50。
- `all` 模式选择 Library 中全部含 Book 的 Series。
- 每个 Series 只取自然排序第一本 Book 的第一页；有效封面直接跳过。
- 全局单 worker 串行执行。单个 Series 失败只记录有限且脱敏的错误摘要，后续继续。
- 任务进度持久化在 `thumbnail_runs`，支持取消；服务重启会把未完成任务标记失败。

Range/Page 缓存使用 SQLite 中的 `last_access_at` 按最久未使用顺序淘汰。并发读取
同一缓存键时由 inflight flight 合并。

### 下一卷索引预读

`volume_index_prefetch_enabled` 默认关闭；
`volume_index_prefetch_remaining_pages` 默认 10，管理 API 限制为 1–100。

具体页面成功返回后，HTTP 层把当前 Book、页码和总页数提交给
`VolumeIndexPrefetcher`。达到阈值时，它按
`number_sort, name COLLATE NOCASE, id` 查找同一 Series 的下一本 Book，并仅调用
`ListPages` 建立 ZIP central directory 或 RAR 文件头索引，不调用 `ReadPage`。

预读器只有一个 worker，因此全局串行；queued 和 active 使用 Book ID 加文件版本
去重。One-Shots、单 Book Series、Series 最后一本以及未达到阈值的请求直接跳过。
失败只记录不含 URL、Token 和用户路径的分类日志，不影响当前页面响应。

### 阅读进度

Mihon 的 Komga Tracker 使用 `/api/v2/series/{seriesId}/read-progress/tachiyomi`：

- `PUT` 请求体只包含 `lastBookNumberSortRead`，表示自然顺序中
  `number_sort <= lastBookNumberSortRead` 的现有 Book 已读。服务端严格以该值覆盖
  当前 Series 的 Book 级已读状态，允许前进和回退。
- `GET` 返回 `booksCount`、`booksReadCount`、`booksUnreadCount`、
  `booksInProgressCount`、`lastReadContinuousNumberSort` 和 `maxNumberSort`。
  `booksInProgressCount` 不受推断页码影响，当前始终为 0。

页面图片成功写入响应后，HTTP 层调用 `RecordBookPageProgress` 记录该 Book 的
`last_loaded_page`、`max_loaded_page`、`page_count` 和更新时间。这个数据来自图片加载
请求，可能被 Mihon 预读推进，因此不能用于自动标记 Book 已读，也不能反向恢复 Mihon
本地的页码位置。

### 详情页维护任务和性能统计

`internal/maintenance` 提供一个全局串行 worker，处理 Series / Book 信息页发起的
维护任务：

- `book_index`：为当前 Book 建立归档索引。
- `series_index`：为当前 Series 的全部 Book 建立归档索引。
- `series_thumbnail`：用当前 Series 第一本 Book 的第一页生成系列封面。

默认模式会跳过当前文件版本已有的索引或有效封面；强制模式会重新读取并覆盖。
强制按钮在前端使用警示样式和二次确认。任务进度写入 `maintenance_runs`，包括
总数、已处理、生成、跳过、失败、当前条目、有限错误摘要和取消标记。取消不会主动
中断当前正在处理的条目，而是在条目结束后尽快停止后续条目；服务重启会把未完成
任务标记为失败。

Series 信息页显示当前版本已建索引数量、平均索引耗时、最近索引完成时间、系列封面
生成耗时和该 Series 下真实 Range 下载平均速度。Book 信息页显示当前 Book 的索引
状态、索引耗时和真实 Range 下载平均速度。管理页状态区域只显示全局平均索引耗时、
平均封面耗时和真实 Range 下载平均速度。

## 11. HTTP 层

路由分为：

- `/admin`：内嵌管理页面。
- `/series/{id}`、`/book/{id}`、`/books/{id}`：Mihon WebView 使用的本地信息页。
- `/admin/api/*`：账号、Library、设置、扫描、封面任务、维护任务和缓存管理。
- `/admin/api/maintenance-jobs`：详情页手动封面/索引维护任务的创建、查询和取消。
- `/api/v1/*`：Komga 兼容的 Library、Series、Book、Pages 和图片端点。
- `/api/v2/series/{id}/read-progress/tachiyomi`：Mihon 所需的简化阅读进度端点。
- `/health`：健康检查。

Komga API 是有意裁剪的兼容层。集合、元数据和分页字段以 Mihon 实际需要为准。
未实现的缩略图返回占位图；collection/readlist 等返回空集合。

WebView 信息页初始渲染只查询 SQLite，并通过现有 Series thumbnail 端点显示已缓存
封面。普通 Book 页面不显示封面，One-Shots Book 页面可以显示已有封面；打开信息页
本身不触发 downurl、归档索引或页面读取。只有用户点击手动维护按钮后，后台任务才会
按需读取归档索引或系列第一本第一页。Series / Book 信息页会显示 Mihon 同步的卷级
已读状态，以及从页面图片请求推断出的最近加载页和最大加载页。该推断页码可能包含
Mihon 预读页面，只用于展示，不影响 Tracker 的已读状态。浏览器路由的 404 返回自适应
HTML，API 路由的 404 仍保持 JSON。

## 12. 错误映射

归档错误在 HTTP 层转换为稳定状态码：

- Range 不支持：501
- 页面超过限制：413
- 损坏或不支持的 ZIP/RAR：422
- 不支持的归档类型：415
- 请求取消：499
- 其他远程读取错误：502

不要把包含敏感 URL 或 Token 的底层错误直接返回给客户端。

## 13. 测试策略

- `internal/archive/*_integration_test.go` 使用本地 HTTP Range mock。
- `internal/httpserver/server_integration_test.go` 验证 Mihon/Komga 合同。
- `internal/scanner/manager_test.go` 验证 One-Shots 和事务式模式转换。
- `internal/thumbnail/batch_test.go` 验证最新 N 选择、跳过、失败继续和取消。
- `internal/maintenance/manager_test.go` 验证详情页维护任务的生成、跳过和取消。
- `internal/database/migrations_test.go` 从旧 schema 验证升级。
- `internal/cache`、`internal/id`、`internal/natsort` 和 `internal/thumbnail` 有单元测试。

测试不需要真实 115 账号，也不应连接真实私人服务。

## 14. 发布

推送 `v*` Tag 会触发两个发布工作流。

`.github/workflows/release.yml` 先执行测试和 vet，然后以 `CGO_ENABLED=0`
交叉编译 Windows amd64、Linux amd64/arm64、macOS amd64/arm64。每个平台产物和
README、LICENSE 一起打包，最后生成 `SHA256SUMS` 并创建 GitHub Release。

`.github/workflows/docker.yml` 使用 Docker Buildx 构建 `linux/amd64` 和
`linux/arm64` 多架构镜像，并推送到 `blacktal/fake-komga-115`。镜像标签包括
`latest`、原始 Git Tag（如 `v0.1.10`）、不带 `v` 的完整语义版本（如 `0.1.10`）
以及 minor 标签（如 `0.1`）。Docker Hub 凭据只来自 GitHub Actions secrets，
不要写入仓库。

GitHub Release 的正文不再只依赖自动生成说明。发布 `vX.Y.Z` 前必须提交
`docs/release-notes/vX.Y.Z.md`；Release 工作流会检查该文件并用它作为发布说明，缺失
时直接失败。

二进制 Release 和 Docker 构建都通过 `-ldflags -X` 把 Tag 写入
`internal/buildinfo.Version`；普通源码构建显示开发版本。
