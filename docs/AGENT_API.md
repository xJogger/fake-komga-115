# Agent API 参考手册 (`/api/agent/v1`)

**MangaAlpha** 内置了专为 AI Agent（Claude Code、Codex、Cursor、Antigravity、MCP Server、自动化运维脚本等）设计的管理接口：`/api/agent/v1/*`。

相比面向前端与阅读客户端的完整 Komga 分页结构，`/api/agent/v1` 具有以下特点：

1. **自描述能力发现**：`GET /api/agent/v1/manifest` 一次性返回当前版本、支持的存储源、刮削模式与端点目录。
2. **标准鉴权头**：同时支持 `Authorization: Bearer <API_KEY>`、`X-API-Key: <API_KEY>` 与 `HTTP Basic Auth`。
3. **高信噪比紧凑 JSON**：`GET /api/agent/v1/series` 省略冗余空字段与前端布局包装，以最少 Token 返回目录名、相对路径、Bangumi 匹配状态、置信度、作者与 NSFW 标记。
4. **管理视角可见性**：即使前台设置 `show_nsfw = false` 隐藏了 NSFW 漫画，Agent 默认（`nsfw=all`）仍可检索、审查和修正 NSFW 条目；同时可通过 `include_hidden=true` 查看已隐藏书库。
5. **单次批量修正**：`POST /api/agent/v1/series/batch` 支持单次提交最多 200 条异构操作（绑定 Bangumi ID、修改并锁定标题/NSFW/分类、自动重刮、解绑），大幅减少工具调用轮次。

---

## 1. 认证准备

1. 登录网页管理后台 `/admin?tab=security`（「账号与 API Key」）。
2. 创建一个专属 API Key（例如备注 `Library-Agent`）。
3. 在所有请求中携带以下任一请求头：

```http
Authorization: Bearer fk115_xxxxxxxxxxxxxxxxxxxxxxxx
```

或：

```http
X-API-Key: fk115_xxxxxxxxxxxxxxxxxxxxxxxx
```

---

## 2. 端点速查

### 2.1 能力清单与全局概览

- `GET /api/agent/v1/manifest`：返回 API 版本、鉴权方式、支持的 `providers`、`scrapeModes` 与完整端点说明。
- `GET /api/agent/v1/overview`：返回有效/隐藏书库数、可见系列与单册数、NSFW 系列数、当前 `showNsfw` 开关状态及 Bangumi 离线索引与匹配统计。

```bash
curl -fsS -H "Authorization: Bearer $FK115_API_KEY" \
  http://127.0.0.1:25600/api/agent/v1/overview
```

### 2.2 书库管理与扫描

- `GET /api/agent/v1/libraries?include_hidden=true`：列出全部书库（默认包含已隐藏书库，`hidden: true` 表示已隐藏）。
- `POST /api/agent/v1/libraries`：创建书库：
  - 115 网盘：`{"name": "日漫连载", "providerType": "115", "rootCid": "123456", "oneShot": false}`
  - 本地 / SMB 挂载目录：`{"name": "NAS 漫画", "providerType": "local", "rootPath": "/share/manga", "oneShot": false}`
  - WebDAV（CloudDrive2 / OpenList / NAS）：`{"name": "CloudDrive2 书库", "providerType": "webdav", "webdavAccountId": "<id>", "rootPath": "/comics"}`
- `PATCH /api/agent/v1/libraries/{libraryID}`：局部更新书库名称、隐藏状态或单本模式：
  ```json
  { "name": "日漫归档", "hidden": true }
  ```
  > 设为 `hidden: true`（等价于 `enabled: false`）会保留全部系列、元数据、阅读进度与封面缓存，并立即取消该书库排队/运行中的任务。
- `POST /api/agent/v1/libraries/{libraryID}/scan`：触发指定书库目录扫描（已隐藏书库会拒绝启动）。
- `POST /api/agent/v1/scans`：`{"libraryId": ""}` 触发全部未隐藏书库扫描。
- `DELETE /api/agent/v1/libraries/{libraryID}`：删除书库索引记录（不会删除网盘或磁盘原始文件）。

### 2.3 系列检索与详情审查

- `GET /api/agent/v1/series`：紧凑分页检索，支持查询参数：
  - `page`（从 `0` 开始，默认 `0`）、`size`（默认 `50`，最大 `200`）
  - `search` / `q`：搜索显示标题、原始目录名、别名或作者
  - `library_id`：限定书库 ID（可重复传参或逗号分隔）
  - `scrape_status`：`none`（尚未刮削）| `matched`（已匹配，含手动绑定）| `unmatched`（自动未命中）
  - `nsfw`：`all`（默认，含 NSFW）| `only`（仅 NSFW）| `exclude`（排除 NSFW）
  - `include_hidden`：`true` | `false`（默认 `false`，仅查未隐藏书库）
  - `publisher`、`genre`、`tag`、`author`、`status`、`sort`
- `GET /api/agent/v1/series/{seriesID}`：查看单部系列的完整 `metadata`（含全部 `*Lock` 字段锁、`isNsfw`、`nsfwLock`）、阅读进度与单册文件列表。
- `GET /api/agent/v1/series/{seriesID}/bangumi/candidates?q=关键词或SubjectID`：检索该系列的 Bangumi 候选条目及目录名清洗结果 `parsed`。

### 2.4 批量修正系列元数据与 Bangumi 绑定

`POST /api/agent/v1/series/batch` 支持两种调用格式（单次最多 200 条）：

#### 方式 A：异构逐条操作（适合 Agent 批量巡检纠错）

```bash
curl -fsS -X POST \
  -H "Authorization: Bearer $FK115_API_KEY" \
  -H "Content-Type: application/json" \
  http://127.0.0.1:25600/api/agent/v1/series/batch \
  -d '{
    "operations": [
      {
        "seriesId": "s_abc123",
        "action": "bind_bangumi",
        "subjectId": 539
      },
      {
        "seriesId": "s_def456",
        "action": "patch_metadata",
        "patch": {
          "title": "水星领航员（完全版）",
          "titleLock": true,
          "isNsfw": false,
          "nsfwLock": true
        }
      },
      {
        "seriesId": "s_ghi789",
        "action": "auto_scrape"
      }
    ]
  }'
```

#### 方式 B：同构批量操作

```json
{
  "action": "auto_scrape",
  "seriesIds": ["s_1", "s_2", "s_3"]
}
```

支持的 `action`：
- `bind_bangumi`：绑定指定 Bangumi `subjectId`（优先拉取实时详情，离线库兜底，保留已锁定字段）。
- `auto_scrape`：按最新清洗与繁简规则重新自动匹配单部作品。
- `patch_metadata`：局部修改标题、排序名、状态、方向、出版社、简介、分类、标签、封面来源（`auto`/`bangumi`/`book`）、`isNsfw` 及对应 `*Lock` 锁。
- `clear_bangumi`：解除 Bangumi 绑定并恢复目录名标题。

### 2.5 Bangumi 批量刮削、任务监控与全局开关

- `POST /api/agent/v1/bangumi/scrape`：
  ```json
  {
    "libraryId": "",
    "mode": "retry_unmatched"
  }
  ```
  `mode` 可选：
  - `incremental`：仅刮削新增未处理（`none`）系列
  - `retry_unmatched`：重新刮削 `unmatched` 与新增 `none` 系列（跳过已匹配系列）
  - `force`：全量重新自动匹配（仍保护手动绑定与已锁定字段）
- `POST /api/agent/v1/bangumi/archive/update`：后台检查并更新官方 `bangumi/Archive` 本地离线索引包（默认比对快照名称/摘要，未更新自动跳过下载；可选传 `{"force": true}` 强制重下；导入新版后自动本地增量同步已匹配作品的未锁定元数据）。
- `GET /api/agent/v1/bangumi/archive/schedule` / `PUT /api/agent/v1/bangumi/archive/schedule`：读取或设置 Bangumi 离线库定时更新（支持 `weekly` / `monthly`，按服务器本机时区执行）。
- `GET /api/agent/v1/diagnostics` / `PUT /api/agent/v1/diagnostics/config` / `GET /api/agent/v1/diagnostics/export` / `DELETE /api/agent/v1/diagnostics`：查看、切换详细模式、导出或清空全平台脱敏诊断日志。
- `GET /api/agent/v1/bangumi/archive/source` / `PUT /api/agent/v1/bangumi/archive/source`：查询或切换 Bangumi 离线索引库数据源（支持 `zh_cn` 中文简介增强镜像 [默认]、`official` 官方原版或 `custom` 自定义 `aux/latest.json` URL）。
- `GET /api/agent/v1/update` / `POST /api/agent/v1/update/check` / `PUT /api/agent/v1/update/config`：查询软件版本与更新检测状态、触发手动检查或调整 24 小时后台自动检查/版本忽略配置。
- `GET /api/agent/v1/jobs?limit=20`：汇总查看扫描任务、Bangumi 刮削任务、离线库更新状态、封面任务与维护任务。
- `POST /api/agent/v1/jobs/{jobType}/{runID}/cancel`：取消任务（`jobType` 支持 `scan`、`bangumi`、`cover`）。
- `GET /api/agent/v1/settings` / `PATCH /api/agent/v1/settings`：读取或修改系统参数（例如 `{"showNsfw": false}` 或 `{"show_nsfw": true}`）。

---

## 3. 推荐的 Agent 书库治理工作流

1. 调用 `GET /api/agent/v1/overview` 了解书库规模、离线索引库是否就绪（`bangumi.archive.ready`）以及未命中数量。
2. 如规则升级或新增书库后存在未命中条目，调用 `POST /api/agent/v1/bangumi/scrape`（`{"mode": "retry_unmatched"}`），随后间隔数秒检查 `GET /api/agent/v1/jobs` 直至完成。
3. 分页拉取剩余未命中条目：`GET /api/agent/v1/series?scrape_status=unmatched&size=50&page=0`。
4. 对疑难目录调用 `GET /api/agent/v1/series/{id}/bangumi/candidates?q=...` 确认准确的 `subjectId`，再通过 `POST /api/agent/v1/series/batch` 一次性批量提交绑定或标题修正。
