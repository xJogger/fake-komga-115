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
       └── generated series cover ─────── internal/thumbnail
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

`internal/app.New` 依次创建 Store、Cache Manager、115 Client、Scanner、Archive
Service、Thumbnail Service 和 HTTP Server。关闭时先停止扫描上下文，再关闭数据库。

## 3. SQLite 数据模型

主要表：

- `settings`：扫描、限速、Range block、缓存和预读设置。
- `provider_accounts`：单个 115 账号的 Access/Refresh Token。
- `libraries`：115 根目录和普通/One-Shots 模式。
- `scan_runs`：扫描队列、进度、结果和取消标记。
- `series`：Komga Series 元数据及 `seen_scan_id`。
- `books`：115 漫画归档文件元数据及 `seen_scan_id`。
- `scan_series_staging` / `scan_books_staging`：模式转换的临时完整快照。
- `zip_indexes`：ZIP 和 RAR 共用的页面索引 JSON。
- `series_thumbnails`：生成封面的文件信息和源 Book 版本。
- `downurl_cache`：短期 downurl 和 User-Agent。
- `cache_entries`：Range/Page 磁盘缓存的大小与访问时间。

数据库启用 WAL、foreign keys 和 busy timeout。新字段必须同时更新 fresh schema 和
升级迁移。当前迁移通过 `PRAGMA table_info` 判断缺失列。

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
SHA1 和修改时间。

### Thumbnail

用户请求 Series 第一本 Book 的第一页后异步生成。输入像素数有限制，输出 JPEG
质量 75，最长边 300px。数据库记录源 Book 和版本，源变化后自动失效。

Range/Page 缓存使用 SQLite 中的 `last_access_at` 按最久未使用顺序淘汰。并发读取
同一缓存键时由 inflight flight 合并。

## 11. HTTP 层

路由分为：

- `/admin`：内嵌管理页面。
- `/admin/api/*`：账号、Library、设置、扫描和缓存管理。
- `/api/v1/*`：Komga 兼容的 Library、Series、Book、Pages 和图片端点。
- `/api/v2/series/{id}/read-progress/tachiyomi`：Mihon 所需的简化阅读进度端点。
- `/health`：健康检查。

Komga API 是有意裁剪的兼容层。集合、元数据和分页字段以 Mihon 实际需要为准。
未实现的缩略图返回占位图；collection/readlist 等返回空集合。

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
- `internal/database/migrations_test.go` 从旧 schema 验证升级。
- `internal/cache`、`internal/id`、`internal/natsort` 和 `internal/thumbnail` 有单元测试。

测试不需要真实 115 账号，也不应连接真实私人服务。
