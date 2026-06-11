# AGENTS.md

本文档面向 AI 编程助手和参与二次开发的贡献者。开始修改前，先阅读
`README.md`、`SECURITY.md` 和 `docs/ARCHITECTURE.md`。

## 项目目标

这是一个轻量 Komga 兼容层，不是真实 Komga，也不是 115 挂载工具。核心约束：

1. 扫描阶段只读取 115 目录和文件元数据，不读取漫画内容。
2. 只有用户请求页面时，才通过 downurl 和 HTTP Range 按需读取归档字节。
3. 不在本地永久保存完整漫画文件。
4. 兼容 Mihon Komga 扩展所需的 API，而不是完整实现 Komga。
5. 当前安全模型是可信局域网单用户、整个服务免认证。

不要在没有明确需求时改变这些约束。

## 开发命令

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
go build -o /tmp/fake-komga-115 ./cmd/server
```

提交前必须至少执行 `go test ./...`。涉及归档、数据库、扫描或 HTTP DTO 时，还要
运行 `go vet ./...` 和构建命令。

## 目录职责

- `cmd/server`：CLI 参数、信号处理和进程生命周期。
- `internal/app`：组装数据库、115 客户端、扫描器、缓存、归档和 HTTP 服务。
- `internal/oneonefive`：115 SDK 封装、Token 刷新、限速、目录列表和 downurl。
- `internal/database`：SQLite schema、迁移、查询和持久化模型。
- `internal/scanner`：递归扫描、扫描状态、普通模式和 One-Shots 模式。
- `internal/archive`：远程 ReaderAt、ZIP/RAR 索引与按页解压。
- `internal/cache`：Range block 和已解压页面的磁盘缓存及 LRU 式淘汰。
- `internal/thumbnail`：阅读触发的系列封面生成和手动批量封面任务。
- `internal/httpserver`：管理 API、管理页面和 Komga 兼容 API。
- `internal/id`：稳定、可逆的 Library/Series/Book ID。
- `internal/natsort`：漫画文件和页面的自然排序。

## 不可破坏的行为

### 扫描

- 普通模式：有直接漫画文件的每个目录是一个 Series；只有子目录的目录被忽略。
- One-Shots：递归忽略目录层次，每个支持的归档文件独立成为 Series。
- 支持的归档扩展名仅为 CBZ、ZIP、CBR、RAR。
- 文件大小来自列表元数据，用于库容量统计；扫描不能读取归档内容。
- 正常重扫可增量 upsert，只有扫描成功后才能删除未再次出现的旧记录。
- 普通模式与 One-Shots 互相转换时必须使用 staging 表；失败、取消或重启必须
  保留上一次完整结构。

### 懒加载

- 列出 Series/Book 不得触发 downurl 或归档读取。
- 列出 Pages 可以读取归档索引所需的 Range。
- 请求具体页面时只读取该页所需的数据块。
- downurl 不支持 Range 时返回明确错误，不得静默下载整本漫画。

### 缓存和封面

- Range、Page、归档索引、系列封面必须可分别清理。
- Range/Page 上限为 0 时表示不限制。
- 封面只由 Series 第一本 Book 的第一页生成，最长边 300px，JPEG 质量 75。
- 手动封面任务必须全局串行，已有有效封面跳过，单 Series 失败不能中断后续任务。
- 最新 N 封面按 Series 中最新 Book 的 115 文件修改时间选择，默认 N 为 50。
- 全库封面按钮必须保持警示样式和二次确认，并提供持久化进度与取消。
- 缓存键必须包含足够的文件版本信息，避免文件变化后读取旧数据。

### API

- 保持 Komga DTO 字段名、分页结构、时间格式和 `oneshot` 语义。
- 未实现的图片端点应返回占位图；适合空响应的集合端点返回空集合。
- 不要随意删除现有端点。新增行为需要相应集成测试。

## 数据库修改规则

1. 新安装的完整 schema 写入 `internal/database/schema.go`。
2. 已有数据库需要的变更写入显式迁移，不能只修改 `CREATE TABLE IF NOT EXISTS`。
3. 迁移必须可重复执行。
4. 删除 Library/Series/Book 时检查外键级联对索引、封面和缓存元数据的影响。
5. 时间统一保存为 UTC RFC3339Nano。

## 安全规则

- 绝不把 Token、Authorization、Cookie、真实 downurl 或用户文件路径写入日志。
- 测试只能使用虚构 Token、CID、File ID、Pick Code 和 URL。
- 不要提交 `data/`、数据库、缓存、日志、二进制、`.env` 或 `user_info`。
- 新增管理字段时，GET API 不得返回 Token 原文。
- 当前服务免认证；任何“公网可用”描述都必须明确依赖外部认证和 TLS。

## 测试建议

- 数据库变更：添加新库和旧 schema 升级测试。
- 扫描规则：测试普通模式、One-Shots、同名文件、失败转换和取消。
- ZIP/RAR：使用本地 mock HTTP Range 服务，不依赖真实 115 账号。
- HTTP：通过 `httptest` 验证 Mihon/Komga 所需字段。
- 并发和缓存：优先测试重复请求合并、淘汰和版本失效。

## 修改完成检查表

- [ ] 没有改变按需读取原则。
- [ ] 没有在日志或测试夹具中加入敏感信息。
- [ ] schema 和迁移同时更新。
- [ ] DTO 和过滤参数保持 Komga 兼容。
- [ ] 新行为有测试。
- [ ] `gofmt`、`go test ./...`、`go vet ./...`、构建均通过。
- [ ] README 和 `docs/ARCHITECTURE.md` 与实现一致。
