# Web 漫画客户端 API 说明

本文档面向独立纯前端漫画阅读器和负责开发它的 AI Agent。目标场景是：前端托管在
Cloudflare Pages / GitHub Pages / Cloudflare Workers Static Assets 等静态站点上，
用户浏览器直接访问局域网内的 `fake-komga-115` 后端。

```text
Cloudflare/GitHub 静态前端
        ↓ 用户浏览器 fetch/img
局域网 fake-komga-115：http://192.168.x.x:25600
        ↓ 115 Open + downurl + HTTP Range
远程 CBZ/ZIP/CBR/RAR
```

Cloudflare 或 GitHub 只托管前端文件，不能代替用户浏览器访问 `192.168.x.x` 后端。
不要把 115 Token、downurl、Cookie 或用户本地路径发送到前端托管平台。

## 1. 后端地址与 CORS

用户需要在前端设置后端 Base URL，例如：

```text
http://192.168.26.120:25600
```

建议使用局域网 IP 字面量，不要使用会解析到局域网 IP 的公网域名。浏览器对本地网络
访问的判断和权限提示会更明确。

`fake-komga-115` 默认关闭跨域访问。用户需要在本地管理页：

```text
http://后端地址:25600/admin
```

的“允许的 Web 前端 Origin（CORS）”中添加纯前端站点的 Origin，每行一个，例如：

```text
https://reader.example.pages.dev
https://reader.example.com
http://localhost:5173
```

规则：

- 只支持精确 Origin 匹配。
- Origin 必须是 `http://host[:port]` 或 `https://host[:port]`。
- 不支持 `*`，也不支持 `http://localhost:*`。
- 留空表示关闭 CORS。
- CORS 只对 `/api/v1/*` 和 `/api/v2/*` 生效，不对 `/admin/api/*` 生效。

后端在允许的 Origin 上会返回：

```http
Access-Control-Allow-Origin: <Origin>
Access-Control-Allow-Credentials: true
Access-Control-Allow-Methods: GET,HEAD,POST,PUT,PATCH,DELETE,OPTIONS
Access-Control-Allow-Headers: Content-Type,Accept,Authorization
Access-Control-Allow-Private-Network: true   # 仅当预检请求带 Access-Control-Request-Private-Network: true
```

Chrome / Edge 对公网 HTTPS 页面访问局域网 HTTP 后端可能触发 Local Network Access
权限提示或拦截。前端应准备友好错误提示，引导用户：

1. 确认后端地址能在浏览器直接打开；
2. 确认管理页 CORS Origin 已填写；
3. 使用 IP 字面量后端地址；
4. 按浏览器提示允许本地网络访问。

## 2. 通用约定

- 服务免认证。用户名、密码、API Key 留空。
- JSON API 基础路径：`{baseUrl}/api/v1`。
- 页码类参数：列表分页 `page` 从 `0` 开始；漫画页面 `pageNumber` 从 `1` 开始。
- 时间字段为 UTC RFC3339 字符串。
- 列表响应是 Komga 风格分页对象：

```json
{
  "content": [],
  "totalElements": 0,
  "totalPages": 0,
  "size": 20,
  "number": 0,
  "numberOfElements": 0,
  "first": true,
  "last": true,
  "empty": true
}
```

错误响应：

```json
{
  "error": "ERROR_CODE",
  "message": "Human readable message."
}
```

常见错误：

- `404 NOT_FOUND`：资源不存在。
- `501 RANGE_NOT_SUPPORTED`：115 downurl 不支持 HTTP Range，后端不会整本下载。
- `413 PAGE_TOO_LARGE`：解压后单页超过限制。
- `422 INVALID_ZIP` / `INVALID_RAR` / `SOLID_RAR_NOT_SUPPORTED` /
  `ENCRYPTED_RAR_NOT_SUPPORTED` / `MULTI_VOLUME_RAR_NOT_SUPPORTED`：归档不支持或损坏。
- `502 REMOTE_READ_FAILED`：远程读取失败。

## 3. 能力探测

```http
GET /api/v1/server/capabilities
```

示例响应：

```json
{
  "name": "fake-komga-115",
  "version": "v1.3.0",
  "apiBasePath": "/api/v1",
  "compatibility": "komga-partial",
  "corsConfigured": true,
  "features": {
    "libraries": true,
    "seriesList": true,
    "booksList": true,
    "bookPages": true,
    "pageImages": true,
    "seriesThumbnails": true,
    "pageReadProgress": true,
    "deleteBookReadProgress": true,
    "bookSiblingNavigation": true,
    "clientSettings": true,
    "privateNetworkAccessHeader": true
  }
}
```

前端启动时可以先请求该接口；如果失败，应提示用户检查后端地址、CORS 和本地网络权限。

## 4. 书库、系列和 Book

### 列出 Library

```http
GET /api/v1/libraries
```

返回数组。常用字段：

```json
{
  "id": "libraryId",
  "name": "YAAS",
  "oneshotsDirectory": "."
}
```

`oneshotsDirectory` 为 `.` 表示该库处于 One-Shots 模式。

### 列出 Series

简单 GET：

```http
GET /api/v1/series?page=0&size=50&library_id=<libraryId>&search=<keyword>&sort=lastModifiedDate,desc
```

支持参数：

- `page`、`size`
- `library_id`：可重复或逗号分隔
- `search`
- `read_status=UNREAD|IN_PROGRESS|READ`
- `oneshot=true|false`
- `sort`：建议使用
  - `metadata.titleSort,asc`
  - `metadata.titleSort,desc`
  - `lastModifiedDate,desc`
  - `createdDate,desc`
  - `random`

结构化搜索：

```http
POST /api/v1/series/list?page=0&size=50&sort=lastModifiedDate,desc
Content-Type: application/json

{
  "fullTextSearch": "keyword",
  "condition": {
    "allOf": [
      {"deleted": {"operator": "isFalse"}},
      {"libraryId": {"operator": "is", "value": "libraryId"}},
      {"oneShot": {"operator": "isFalse"}}
    ]
  }
}
```

Series 常用字段：

- `id`
- `libraryId`
- `name`
- `booksCount`
- `booksReadCount`
- `booksUnreadCount`
- `booksInProgressCount`
- `metadata.title`
- `metadata.titleSort`
- `fileLastModified`
- `oneshot`

系列封面：

```text
GET /api/v1/series/{seriesId}/thumbnail
```

没有封面时会返回占位图片。

### Series 详情与 Book 列表

```http
GET /api/v1/series/{seriesId}
GET /api/v1/series/{seriesId}/books?unpaged=true&sort=metadata.title,asc
```

Book 常用字段：

- `id`
- `seriesId`
- `seriesTitle`
- `libraryId`
- `name`
- `number`
- `sizeBytes`
- `fileHash`
- `media.status`
- `media.mediaType`
- `media.pagesCount`
- `metadata.title`
- `metadata.numberSort`
- `readProgress`
- `oneshot`

注意：`media.pagesCount` 只有归档索引已经生成后才准确；首次打开 Book 的页面列表会触发
远程归档索引读取。

### Book 搜索

```http
GET /api/v1/books?page=0&size=50&library_id=<libraryId>&read_status=IN_PROGRESS&sort=lastModifiedDate,desc
POST /api/v1/books/list?page=0&size=50&sort=metadata.title,asc
```

`POST /api/v1/books/list` 的结构化条件同 Series，支持 Library、One-Shot、Read Status、
全文搜索。由于本项目没有真实 genre/tag/author/publisher/collection 元数据，这些筛选会
返回空结果，而不是忽略条件。

## 5. 阅读器流程

推荐流程：

1. 用户选择 Library 或搜索 Series。
2. 进入 Series，拉取 `/series/{seriesId}/books?unpaged=true`。
3. 进入 Book，拉取 `/books/{bookId}`。
4. 拉取 `/books/{bookId}/pages` 获取页面列表。
5. 按需把图片地址设置到 `<img>`：

```text
{baseUrl}/api/v1/books/{bookId}/pages/{pageNumber}
```

或：

```text
{baseUrl}/api/v1/books/{bookId}/pages/{pageNumber}/raw
```

两者当前等价。

重要约束：

- 不要一次性 fetch 整本所有图片。
- 使用懒加载，例如 IntersectionObserver 或单页翻页只加载当前页附近几页。
- `/books/{bookId}/pages` 会读取归档索引；具体图片请求才读取并解压对应页面。
- 图片 URL 可以直接用于 `<img src="...">`。如需通过 `fetch()` 转成 Blob，仍会受 CORS 和
  Local Network Access 影响。

## 6. 页级阅读进度

### 获取进度

```http
GET /api/v1/books/{bookId}/read-progress
```

无记录时返回 `null`。有记录时：

```json
{
  "completed": false,
  "page": 12,
  "readDate": "2026-08-07T08:00:00Z"
}
```

### 更新进度

```http
PATCH /api/v1/books/{bookId}/read-progress
Content-Type: application/json

{"page": 12}
```

或：

```json
{"page": 120, "completed": true}
```

规则：

- `page` 是 1-based。
- `completed=true` 表示该 Book 已读。
- `completed=false` 或只传 `page` 表示阅读中。
- 前端建议当前页稳定进入视野 1 到 2 秒后再同步，避免快速滑动造成频繁写入。
- 到最后一页时可自动发送 `completed=true`，也可以让用户手动确认。

### 标记未读

```http
DELETE /api/v1/books/{bookId}/read-progress
```

该接口只删除真实同步进度，不删除从图片加载请求推断出的页面访问记录。

## 7. 上一卷 / 下一卷

```http
GET /api/v1/books/{bookId}/previous
GET /api/v1/books/{bookId}/next
```

返回相邻 Book 的 DTO。当前 Book 是第一本/最后一本，或 Series 是 One-Shots 时返回 `404`。

前端可以用它实现：

- 阅读末尾跳下一卷；
- 阅读开头跳上一卷；
- 深链接打开单本时不必先拉取整个 Series 列表。

## 8. 客户端设置

前端可以使用 Komga 风格 client settings 保存非敏感偏好：

```http
GET /api/v1/client-settings/user/list
PATCH /api/v1/client-settings/user
```

只允许 key 以 `webui.` 或 `koharia.` 开头，并拒绝包含 token、authorization、cookie、
password 的 key。value 最大 8192 字节。

示例：

```http
PATCH /api/v1/client-settings/user
Content-Type: application/json

{
  "webui.reader.mode": {"value": "paged", "allowUnauthenticated": true},
  "webui.reader.preloadPages": {"value": "2", "allowUnauthenticated": true}
}
```

删除某个设置：

```json
{
  "webui.reader.mode": {"value": null}
}
```

不要保存后端地址、Token、Cookie、密码或其他敏感内容。后端地址应保存在浏览器本地
`localStorage` 或由用户每次填写。

## 9. 前端体验建议

- 默认提供“后端地址测试”按钮，请求 `/api/v1/server/capabilities`。
- 出错时区分：后端不可达、CORS 失败、本地网络权限失败、API 返回 JSON 错误。
- 支持手机竖屏优先，阅读页隐藏非必要 UI。
- 单页模式建议预加载当前页前后 1 到 2 页；连续滚动模式只让可见区域附近图片进入 DOM。
- 不要通过 Service Worker 离线缓存漫画图片。跨 HTTPS 前端和局域网 HTTP 后端时，Service
  Worker 可能受到更严格的本地网络访问限制。
- 不要实现“批量下载整本”或“全库预缓存”。这不符合本项目按需读取原则。
