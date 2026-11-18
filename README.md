# fake-komga-115

[![CI](https://github.com/xJogger/fake-komga-115/actions/workflows/test.yml/badge.svg)](https://github.com/xJogger/fake-komga-115/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/xJogger/fake-komga-115?display_name=tag)](https://github.com/xJogger/fake-komga-115/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

<p align="center">
  <img src="docs/images/readme-hero.png" alt="fake-komga-115 多源按需流式漫画书库与内置阅读体验" width="920">
</p>

`fake-komga-115` 是一个 **100% 纯 Go（`CGO_ENABLED=0`）自包含**的轻量级 Komga 兼容漫画书库与阅读服务端。它原生支持 **115 Open API**、**服务器本地 / SMB 挂载目录** 与 **WebDAV（CloudDrive2 / OpenList / NAS）** 三类存储源共存，内置 **响应式网页书架**、**简体中文双页网页阅读器**、**Bangumi (`bgm.tv`) 离线+在线双引擎元数据刮削中心** 与 **AI Agent 专用书库治理接口**，同时完整兼容 **Mihon（Komga 扩展）** 与 **Koharia** 客户端。

```text
内置网页书架 (/) · 内置简体中文阅读器 (/reader/*) · Mihon / Koharia · AI Agent (/api/agent/v1)
                ↓ (Session Cookie / HTTP Basic Auth / Bearer & X-API-Key)
                              fake-komga-115
        ├── 115 Open API ─────────→ downurl + HTTP Range 分块按需流式解压
        ├── WebDAV (CD2/OpenList) ─→ PROPFIND + 令牌桶限速 + HTTP Range 按需解压
        └── 本地 / SMB 挂载目录 ───→ 原生 io.ReaderAt 随机直读（免分块缓存 + 空挂载点保护）
```

> **按需流式核心承诺**：启动与目录扫描阶段仅读取目录层级及文件大小/修改时间元数据，**绝不提前下载整包压缩文件**；仅当读者打开某一卷并翻到具体页面时，才按需读取对应图片所需的字节区间。

---

## 核心特性

### 1. 多存储源共存、SMB 挂载优化与一键隐藏书库
- **115 网盘直连**：基于 115 Open API 获取目录与短期 `downurl`，通过 `Range` 分块缓存按页流式读取；从 `v1.x` 原地升级可 **100% 保留原有书库 ID、几万部系列记录、阅读进度、归档索引与封面缩略图**。
- **本地磁盘与 SMB 挂载目录**：支持 Linux 本地盘与 CIFS/SMB 挂载目录（如 `/share/manga`、`/mnt/smb`）及 Windows 本地盘、映射网络驱动器（`Z:\`）与 UNC 路径（`\\nas\manga`）。
  - 直接通过操作系统文件句柄（`io.ReaderAt`）按偏移量随机读取，**自动跳过 Range 分块磁盘缓存**，零双份磁盘占用；
  - 针对 SMB 网络文件系统去除多余 `stat` / 符号链接逐级解析开销，自动忽略 `thumbs.db`、`desktop.ini`、`.smbdelete*` 等系统文件；
  - 内置 **SMB 空挂载点防误删保护**：当已有书籍记录的本地/SMB 书库因网络断连或未挂载呈现为空目录时，自动中止扫描并报错，保护既有系列、Bangumi 元数据与阅读进度不被误清空。
- **多账号 WebDAV（CloudDrive2 / OpenList / 群晖 / Nextcloud）**：支持配置多个 WebDAV 账号、自定义每秒请求限速（同时约束 `PROPFIND` 扫描与 `Range` 页面读取，适配 CloudDrive2 / OpenList 挂载网盘防风控）及局域网自签名 HTTPS 证书选项。
- **一键隐藏书库**：支持在后台随时将任意书库设为「已隐藏」或「取消隐藏」。隐藏期间完整保留数据库记录、归档索引与封面缓存，同时在前台书架、客户端 API、直链详情、内置阅读器及扫描/刮削任务中全链路隔离（返回 `404` 并自动中止进行中任务）。

### 2. Bangumi (`bgm.tv`) 双引擎中文刮削与 NSFW 默认隐藏
- **离线快照 + 实时 API 双引擎**：
  - **引擎一（主力批量）**：后台一键下载校验官方 `bangumi/Archive` 公开数据快照并导入本地 SQLite 索引库（自动剔除单行本分卷子条目），数万部漫画可在本地秒级完成批量清洗与匹配，零在线 API 限流风险；启动时支持基于本地快照库自动升级检索词表。
  - **引擎二（精准交互）**：作品详情页支持通过 `api.bgm.tv/v0` 实时关键词搜索、直接粘贴 Subject ID 或 `bgm.tv/subject/...` 链接预览绑定。
  - **独立专用代理**：支持单独为 Bangumi 配置 `http` / `https` / `socks5` / `socks5h` 代理并一键探测延迟，候选封面均经服务端同代理中转缓存，**绝不影响 115 与 WebDAV 直连流量**。
- **深度适配真实漫画命名规则**：
  - 同时兼容 `[标题][作者][出版社][卷数]` 与 `[作者][标题][出版社][扫图组][卷数]` 双序方括号、小写 `x` 合著作者（如 `[镰池和马x冬川基]`）、括号内外双标题（如 `猎魔战记（大剑）`）、中英混排切分（如 `ARIA 水星領航員`）及常见扫图组噪音过滤；
  - 通用子目录自动回溯：对 `台版`、`港版`、`日版`、`特别篇`、`番外篇`、`单行本`、`[CHN]` 等子目录自动向上匹配父系列标题并生成规范后缀（如 `水星领航员（台版）`）；
  - 支持 **增量刮削 / 重试未命中 / 全量强制重刮** 三档批量模式，严格保护手动绑定与 `*Lock` 锁定字段。
- **NSFW 智能匹配与默认隐藏开关**：
  - Bangumi 条目严格依据官方布尔字段 `nsfw: true` 判定分级（不会误伤仅带用户标签但 `nsfw: false` 的普通青年漫画），未命中条目辅助识别显式成人目录关键词，也支持在元数据弹窗中手动开关并锁定 NSFW 标记；
  - 默认全局隐藏 NSFW 作品（前台书架、分类筛选、客户端与阅读直链均自动隔离），可在管理后台随时一键切换显示或隐藏。

### 3. 内置响应式书架与简体中文双页阅读器
- **内置书架（`/`）与管理中心（`/admin`）**：默认自动跟随系统白天/黑夜模式切换（支持手动固定日间/夜间），移动端自适应防横向溢出布局，支持「继续阅读」、最近更新、多维筛选排序，缓存页面全部采用直观的 `MiB` 单位配置与展示，空闲状态零后台轮询刷屏。
- **内置简体中文阅读器（`/reader/*`）**：基于 `Suwayomi-WebUI` 深度定制的无 GraphQL/WebSocket 纯 REST 宿主阅读器，默认界面语言为**简体中文**，纯黑画布护眼背景，桌面端默认日漫右翻双页拼合（宽幅跨页图自动独占一屏），移动端默认单页自适应，支持条漫长图模式、跨卷无缝连读与页级阅读进度双向同步。

### 4. 安全鉴权与 AI Agent 友好接口
- **单管理员强制鉴权**：首次访问（含从 `v1.x` 升级后首次访问）强制引导设置管理员账号密码（`argon2id` 哈希）；浏览器登录会话支持持久自动续期，外部客户端与工具支持 `HTTP Basic Auth`、`Authorization: Bearer <API_KEY>` 与 `X-API-Key: <API_KEY>`。
- **AI Agent 专用 API（`/api/agent/v1`）**：提供自描述能力清单（`GET /api/agent/v1/manifest`）、低 Token 开销系列检索与单次最多 200 条异构批量元数据治理接口，方便接入各类 AI Agent 自动巡检与整理书库。详见 [docs/AGENT_API.md](docs/AGENT_API.md)。

---

## 尚不支持

- 固实（Solid）RAR、加密 RAR、分卷 RAR、7z、加密 ZIP
- 对远程网盘或本地磁盘上的原始文件执行上传、删除、移动或重命名（书库始终只读访问你的漫画源文件）
- 多用户分权体系（当前采用单管理员安全模型）
- 上游 `downurl` 或 WebDAV 服务端不支持 HTTP `Range` 时静默整包下载（会明确返回 `501 RANGE_NOT_SUPPORTED`）

---

## 快速开始

<p align="center">
  <img src="docs/images/quick-start.png" alt="启动、初始化密码、挂载书库并开始阅读" width="920">
</p>

### 方式一：Windows 下载即用

1. 前往 [Releases](https://github.com/xJogger/fake-komga-115/releases/latest) 下载 `windows_amd64.zip` 并解压。
2. 双击运行 `fake-komga-115.exe`，默认浏览器会自动打开 `http://127.0.0.1:25600/` 并引导完成首次管理员用户名与密码设置。
3. 数据默认存储在 `%LOCALAPPDATA%\fake-komga-115\data`。

### 方式二：Linux / macOS 二进制运行

```bash
chmod +x fake-komga-115
./fake-komga-115
```

浏览器打开 `http://127.0.0.1:25600/` 设置管理员账号密码后，进入右上角「管理后台」（`/admin`）添加存储源与书库即可。

### 方式三：Docker 部署

若需读取宿主机本地磁盘或宿主机已挂载的 SMB 共享目录（如 `/share/manga`），将其以 `:ro` 只读方式映射进容器即可：

```bash
docker run -d \
  --name fake-komga-115 \
  --restart unless-stopped \
  -p 25600:25600 \
  -v fake-komga-115-data:/data \
  -v /share/manga:/manga:ro \
  -e TZ=Asia/Shanghai \
  blacktal/fake-komga-115:latest
```

### 方式四：从源码构建

完整构建（含内置 Suwayomi 阅读器静态包）需要 Go 1.26+、Node.js 24+、Python 3 与 `pnpm`（或 `corepack`）：

```bash
git clone https://github.com/xJogger/fake-komga-115.git
cd fake-komga-115
make build
./fake-komga-115
```

### 命令行参数与环境变量

| 参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--host` | `FK115_HOST` | `0.0.0.0` | HTTP 监听地址 |
| `--port` | `FK115_PORT` | `25600` | HTTP 监听端口 |
| `--data-dir` | `FK115_DATA_DIR` | Windows: `%LOCALAPPDATA%\fake-komga-115\data`<br>其他平台: `./data` | SQLite 数据库、Bangumi 索引、封面与缓存目录 |
| `--open-browser` | `FK115_OPEN_BROWSER` | Windows: `true` / 其他: `false` | 启动后是否自动打开浏览器 |
| `--version` | — | — | 打印版本号并退出 |

---

## 存储源、刮削与客户端使用提示

### 配置三类存储源
1. **115 网盘**：可通过 [OpenList 115 授权页](https://api.oplist.org/) 获取 `Refresh Token`，填入「管理后台 -> 存储源 -> 115 开放平台令牌」保存验证，随后在「书库管理」中直接浏览文件夹创建书库。
2. **服务器本地 / SMB 挂载目录**：无需预配账号。Linux 上推荐通过 `cifs` 挂载 NAS 共享（如 `/share/manga`），Windows 上可使用映射盘符（如 `Z:\Manga`）或 UNC 路径（`\\nas\manga`），在「书库管理」中选择「服务器本地 / SMB 挂载目录」即可。
3. **WebDAV（CloudDrive2 / OpenList / NAS）**：
   - **CloudDrive2** 默认 WebDAV 根地址一般为 `http://<IP>:19798/dav`；
   - **OpenList** 默认 WebDAV 根地址一般为 `http://<IP>:5244/dav`；
   - 若后端挂载的是 115 等云盘，建议将 WebDAV 账号的「请求限速」设为 `2 ~ 4` req/s，平稳保护网盘接口。

### Bangumi 刮削与网页端链接说明
- **推荐流程**：首次在「Bangumi 刮削中心」点击「一键下载并构建离线索引库」，就绪后启动批量刮削；后续规则更新时可选择 **「重试未命中」** 模式快速补全未匹配条目。
- **为什么个别漫画点击 `bgm.tv` 链接提示“数据库中没有查询您所指定的条目”？**
  这是 Bangumi 官网的未登录访问限制：部分带有受限标签（如 `R18`，即使官方 `nsfw: false`）的真实条目在未登录浏览器访问时会显示该提示页。只需在浏览器中登录你的 `bgm.tv` 账号后重新打开链接即可正常浏览。

### 连接 Mihon / Koharia 与 AI Agent
- **Mihon / Koharia**：安装 Komga 扩展后填入 `http://<服务器IP>:25600`，可使用管理员账号密码（HTTP Basic）或在「账号与 API Key」中生成的专属 API Key 登录。
- **AI Agent 自动化治理**：携带 `Authorization: Bearer <API_KEY>` 请求 `GET /api/agent/v1/manifest` 即可发现全部管理能力，详见 [docs/AGENT_API.md](docs/AGENT_API.md)。

---

## 从 `v1.x` 升级与备份

升级前建议先停止服务并完整备份 SQLite 主库文件（含 `-wal` / `-shm`）：

```bash
systemctl --user stop fake-komga-115
cp -a data/fake-komga-115.db* /path/to/backup/
# 替换新版本二进制后启动
systemctl --user start fake-komga-115
```

启动时会自动执行幂等数据库迁移，原有 115 书库、阅读进度、ZIP/RAR 索引与封面缓存全部无损继承。

---

## 项目文档

- [docs/AGENT_API.md](docs/AGENT_API.md)：AI Agent 专用书库管理接口（`/api/agent/v1`）参考手册
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)：系统架构、鉴权、多源流式读取、Bangumi 双引擎与内置阅读器原理
- [AGENTS.md](AGENTS.md)：面向开发者与 AI 编程助手的仓库规范与构建指引
- [SECURITY.md](SECURITY.md)：安全模型与敏感信息保护边界

---

## 致谢上游与开源生态

`fake-komga-115` 站在众多优秀开源项目与开放社区的肩膀上，在此向所有上游作者与维护者致以诚挚的感谢：

- **[Suwayomi / Suwayomi-WebUI](https://github.com/Suwayomi/Suwayomi-WebUI)**（MPL-2.0）：提供了体验极其出色的现代化漫画 Web 阅读器前端实现，本项目的内置阅读器（`/reader/*`）基于其源码适配构建。
- **[Komga](https://komga.org/)（[@gotson](https://github.com/gotson)）**：定义了清晰、成熟的漫画数字书库领域模型与 REST API 规范，让跨客户端生态互联成为可能。
- **[Bangumi 番组计划](https://bgm.tv/) 与 [`bangumi/Archive`](https://github.com/bangumi/Archive)**：感谢 Sai 与全体番组计划维基编辑者多年积累的高质量中文 ACGN 资料库，以及官方持续开放的每日离线数据快照与 Open API。
- **[OpenList](https://github.com/OpenListTeam/OpenList) 与 [`OpenListTeam/115-sdk-go`](https://github.com/OpenListTeam/115-sdk-go)**：提供了稳定易用的 115 开放平台 Go SDK、OAuth 授权工具以及优秀的 WebDAV 多存储聚合实践。
- **[CloudDrive2](https://www.clouddrive2.com/)**：提供了高性能的云盘挂载与标准 WebDAV 服务支持，让多种网盘通过 WebDAV 流式接入漫画书库更加便捷。
- **[Mihon](https://mihon.app/) 与 [Koharia](https://github.com/Mister-album/Koharia)**：优秀的移动端与跨平台漫画阅读客户端，推动了 Komga 生态的普及与打磨。
- **Go 核心开源库**：感谢 [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite)（纯 Go 无 CGO SQLite 引擎）、[`nwaples/rardecode`](https://github.com/nwaples/rardecode)（纯 Go RAR4/RAR5 解码器）、[`go-chi/chi`](https://github.com/go-chi/chi) 与 Go 标准库及 `golang.org/x` 团队。

---

## 开源许可

- 主服务代码、内置书架与管理后台前端遵循 [MIT License](LICENSE) 开源。
- 内置网页阅读器（`web/reader/` 与 `/reader/*` 构建产物）基于上游 [Suwayomi-WebUI](https://github.com/Suwayomi/Suwayomi-WebUI) 修改集成，该子模块与衍生适配文件遵循 [Mozilla Public License 2.0 (MPL-2.0)](web/reader/LICENSE)。
