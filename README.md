# fake-komga-115

[![CI](https://github.com/xJogger/fake-komga-115/actions/workflows/test.yml/badge.svg)](https://github.com/xJogger/fake-komga-115/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/xJogger/fake-komga-115?display_name=tag)](https://github.com/xJogger/fake-komga-115/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

<p align="center">
  <img src="docs/images/readme-hero.png" alt="fake-komga-115 多源按需流式漫画书库与内置阅读体验" width="920">
</p>

`fake-komga-115` 是一个纯 Go（`CGO_ENABLED=0`）自包含的轻量级 Komga 兼容漫画服务端。它原生支持 **115 Open API**、**服务器本地目录** 与 **WebDAV** 三类漫画存储源，内置 **响应式网页书架**、**基于 Suwayomi 改造的网页阅读器**、**Bangumi (`bgm.tv`) 中文元数据与封面刮削中心**，同时完整兼容 **Mihon（Komga 扩展）** 与 **Koharia** 客户端。

```text
内置网页书架 / 内置双页阅读器 / Mihon / Koharia
        ↓ (Session Cookie / HTTP Basic Auth / X-API-Key)
  fake-komga-115
        ├── 115 Open API ──→ downurl + HTTP Range 分块按需解压
        ├── WebDAV ────────→ PROPFIND + HTTP Range 分块按需解压
        └── 本地目录 ──────→ 原生 io.ReaderAt 随机直读（零分块缓存冗余）
```

扫描阶段仅读取目录与文件大小/时间元数据，启动或扫描时**绝不下载整包漫画**；只有用户打开某一卷并翻页时，才按需读取对应图片所需的字节区间。

## v2 核心特性

- **单管理员登录鉴权**：首次打开（含从 `v1.x` 原地升级后首次打开）强制引导自定义管理员用户名与密码（`argon2id` 哈希）；未完成设密前拒绝任何业务请求，设密后永久锁定初始化入口。支持浏览器持久会话、`HTTP Basic Auth` 与独立可吊销的 `X-API-Key`。
- **内置响应式网页书架（`/`）与管理后台（`/admin`）**：默认自动跟随系统日夜深浅色模式切换（支持手动切换日间/夜间），开箱即用支持「继续阅读」、最近更新、最新入库、按书库/阅读状态/连载状态/分类/标签/作者/出版社/Bangumi 状态多维筛选与排序，保留 Mihon WebView 详情页归档索引与真实下载速率维护面板，并消除空闲状态下的后台轮询刷屏。
- **内置网页阅读器（`/reader/*`）**：内嵌基于上游 `Suwayomi-WebUI`（MPL-2.0）改造的纯 REST 阅读器，桌面端默认日漫右翻双页拼合（宽幅跨页图自动独占一屏），移动端默认单页自适应，支持条漫长图滚动、快捷键、触控热区、跨卷无缝连读以及与 Komga 接口的页级进度双向同步。
- **Bangumi (`bgm.tv`) 元数据与封面刮削**：
  - **双引擎设计 + 独立代理加速**：内置 `bangumi/Archive` 官方快照本地离线 SQLite 索引引擎（数万部漫画本地极速批量匹配，零 API 封禁风险）+ `api.bgm.tv/v0` 实时搜索与 Subject ID 手动绑定；支持单独为 Bangumi 配置 `http/https/socks5` 代理并测试延迟（不影响 115 / WebDAV 流量）；
  - **智能目录名清洗**：内置繁简/日文汉字归一词表，自动剥离 `[作者][出版社][卷数][完全版/电子版/扫图]` 等复杂命名噪音并结合同目录作者信息交叉打分；
  - **中文优先与按需封面**：优先显示中文译名（保留原目录名可搜索、详情页可查）与 Bangumi 封面（首次访问按需懒加载落盘缓存，支持 `自动 / Bangumi / 首卷图` 一键切换与字段锁定）；
  - **严格禁 NSFW**：离线构建与实时检索全链路过滤 `nsfw=false`。
- **三类存储源共存**：支持同时挂载多个 115 网盘目录、多个本地磁盘目录与多个 WebDAV 账号目录（OpenList / AList / 群晖 / Nextcloud 等），均支持普通多卷目录模式与 One-Shots 单本模式。从 `v1.x` 升级可 100% 保留原有 115 书库 ID、阅读进度、归档索引与缩略图。
- **按需归档读取与智能预读**：支持 ZIP（`store` / `deflate`）与非固实单卷 RAR4/RAR5 按页流式解压、向后翻页预读及下一卷目录索引后台预热。

## 尚不支持

- 固实 RAR、加密 RAR、分卷 RAR、7z、加密 ZIP
- 远程网盘文件的上传、删除、移动或重命名
- 多用户分权（当前为单管理员模型）
- Bangumi NSFW 条目刮削
- 上游 `downurl` 或 WebDAV 服务器不支持 HTTP `Range` 时静默下载整包（会明确返回 `501 RANGE_NOT_SUPPORTED`）

## 快速开始

<p align="center">
  <img src="docs/images/quick-start.png" alt="启动、初始化密码、挂载书库并开始阅读" width="920">
</p>

### Windows：下载后双击

1. 打开 [Releases](https://github.com/xJogger/fake-komga-115/releases/latest) 下载 `windows_amd64.zip` 并解压。
2. 双击 `fake-komga-115.exe`，默认浏览器会自动打开 `http://127.0.0.1:25600/`（首次运行自动跳转 `/setup` 设置管理员用户名与密码）。
3. 数据默认保存在 `%LOCALAPPDATA%\fake-komga-115\data`。

### Linux / macOS：直接运行发布包

```bash
chmod +x fake-komga-115
./fake-komga-115
```

打开浏览器访问 `http://127.0.0.1:25600/` 完成管理员账号初始化，然后进入右上角「管理后台」（`/admin`）配置存储源与书库。

### Docker 部署

推荐使用 `docker-compose.yml`（如果需挂载服务器本地漫画目录，可额外添加只读绑定挂载 `- /path/to/comics:/manga:ro`）：

```bash
docker run -d \
  --name fake-komga-115 \
  --restart unless-stopped \
  -p 25600:25600 \
  -v fake-komga-115-data:/data \
  -v /path/to/local/manga:/manga:ro \
  -e TZ=Asia/Shanghai \
  blacktal/fake-komga-115:latest
```

打开 `http://服务器地址:25600/` 完成首次管理员密码设置即可使用。

### 从源码构建

完整构建（含内置 Suwayomi 阅读器前端产物）需要 Go 1.26+、Node.js 24+、Python 3 与 `pnpm`（或 `corepack`）：

```bash
git clone https://github.com/xJogger/fake-komga-115.git
cd fake-komga-115
make build
./fake-komga-115
```

### 启动参数与环境变量

```text
--host           监听地址，默认 0.0.0.0（环境变量 FK115_HOST）
--port           监听端口，默认 25600（环境变量 FK115_PORT）
--data-dir       数据目录；Windows 默认 %LOCALAPPDATA%\fake-komga-115\data，其他平台默认 ./data（FK115_DATA_DIR）
--open-browser   启动后自动打开网页书架；Windows 默认 true，其他平台默认 false（FK115_OPEN_BROWSER）
--version        输出版本号并退出
```

## 配置存储源与书库

登录并进入「管理后台」（`/admin`）：

1. **115 网盘**：可通过 <https://api.oplist.org/>（115 网盘 OAuth2 授权登录）获取 `Refresh Token` 填入「存储源 -> 115 开放平台令牌」保存验证，随后在「书库管理」中直接可视化浏览 115 文件夹选用创建。
2. **服务器本地目录**：无需配置账号，直接在「书库管理」选择「服务器本地目录」，通过内置目录浏览器选择宿主机/容器内路径即可。
3. **WebDAV**：在「存储源」中添加 WebDAV 服务器地址（如 `https://nas.local:5006/dav` 或 OpenList `/dav`）、用户名与密码并测试连接，随后在「书库管理」中浏览远程目录创建书库。

## Bangumi 元数据刮削指南

- **大规模书库（数千至数万部）**：强烈建议先在「管理后台 -> Bangumi 刮削中心」点击 **「一键下载并构建离线索引库」**。构建完成后点击「开始批量刮削」，数万部漫画可在本地极速完成清洗与匹配，且不会触发在线 API 限流。
- **单部精准修正**：在任意作品详情页（`/series/{id}`）点击 **「Bangumi 刮削 / 匹配」**，可输入任意关键词或直接粘贴 Bangumi 条目 ID / 链接（如 `https://bgm.tv/subject/24596`）预览并绑定；也可点击「编辑元数据」自定义字段并开启字段锁定。

## 连接 Mihon / Koharia 客户端

在 Mihon（安装 Komga 扩展）或 Koharia 中添加服务器：

- **方式一（HTTP Basic 认证）**：填入局域网地址 `http://服务器地址:25600`，并填写你设置的管理员用户名与密码。
- **方式二（API Key 推荐）**：在「管理后台 -> 账号与 API Key」中创建一个专属密钥（如 `Mihon`），在客户端填写 `API Key` 字段，无需暴露主密码且可随时单独吊销。

## 升级与数据备份

从 `v1.x` 升级至 `v2` 前建议先停止服务并备份 `data/fake-komga-115.db*`：

```bash
systemctl --user stop fake-komga-115
cp -a data/fake-komga-115.db* /path/to/backup/
# 替换新版本二进制后启动
systemctl --user start fake-komga-115
```

数据库 schema 迁移会在启动时自动幂等完成，原有 115 书库、阅读进度、归档索引与缩略图均原样保留；升级后首次打开网页按提示设置管理员用户名与密码即可。

## 二次开发与文档

- [AGENTS.md](AGENTS.md)：开发约束、构建流程与目录职责
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)：鉴权、多存储源、Bangumi 双引擎刮削、内置阅读器与缓存架构说明
- [SECURITY.md](SECURITY.md)：安全模型与敏感数据边界

## 致谢与开源许可

- 主服务代码与内置书架前端基于 [MIT License](LICENSE) 开源。
- 内置网页阅读器（`/reader/*`）基于上游 [Suwayomi/Suwayomi-WebUI](https://github.com/Suwayomi/Suwayomi-WebUI) 改造集成，遵循 [Mozilla Public License 2.0 (MPL-2.0)](web/reader/LICENSE)。
- 感谢 [115 Open API](https://www.yuque.com/115yun/open)、[OpenList](https://github.com/OpenListTeam/OpenList)、[Komga](https://komga.org/)、[Bangumi 番组计划 (`bangumi/Archive`)](https://github.com/bangumi/Archive)、[Koharia](https://github.com/Mister-album/Koharia)、[Mihon](https://mihon.app/) 与 [rardecode](https://github.com/nwaples/rardecode)。
