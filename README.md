# fake-komga-115

[![CI](https://github.com/xJogger/fake-komga-115/actions/workflows/test.yml/badge.svg)](https://github.com/xJogger/fake-komga-115/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/xJogger/fake-komga-115?display_name=tag)](https://github.com/xJogger/fake-komga-115/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

<p align="center">
  <img src="docs/images/readme-hero.png" alt="Mihon、fake-komga-115 与 115 云端漫画的按需读取流程" width="920">
</p>

`fake-komga-115` 是一个面向自用场景的 Komga 兼容服务。Koharia 或 Mihon 使用
Komga 兼容源连接它，而漫画目录和 CBZ/ZIP/CBR/RAR 文件实际来自 115 Open API。

```text
Koharia / Mihon → fake-komga-115 → 115 Open API → downurl → HTTP Range → ZIP/RAR 页面
```

它不会运行真实 Komga、挂载 115 网盘或启动时下载漫画文件。扫描只读取目录和
文件元数据；只有用户打开一本漫画时才读取 ZIP central directory 或 RAR 文件头，
翻页时才读取并解压对应图片所需的远程字节范围。

> [!WARNING]
> 当前版本是 `v1.2.0` 开发预览版。服务没有认证功能，115 Token 会明文保存在
> 本地 SQLite 中。仅应部署在可信局域网，不要直接暴露到公网。

## 当前功能

- 单个 115 Open 账号
- 多个 115 根目录，每个根目录映射为一个 Komga Library
- 递归扫描上万目录、十万级文件
- 一个含直接 CBZ/ZIP/CBR/RAR 文件的文件夹映射为一个 Series
- 只有子目录、没有直接漫画文件的目录不生成 Series
- Library 可切换 One-Shots 模式，递归把每个漫画归档文件映射为独立 Series
- 最新 Mihon Komga 扩展和 Koharia 所需的 Series、Book、Pages、筛选和缩略图端点
- Mihon / Koharia WebView 的 Series、Book 信息页、手动维护按钮和友好 HTML 404 页面
- Mihon/Komga Tracker 卷级阅读进度同步，Koharia 页级进度同步，并在信息页显示推断页码进度
- 远程 ZIP central directory 解析
- ZIP `store` 和 `deflate` 页面读取
- RAR4/RAR5 非固实、非加密、单卷页面读取
- Range block、归档索引、解压页面缓存
- 阅读接近当前卷末尾时，可选后台预读同一 Series 下一卷的归档索引
- 阅读一个 Series 第一本漫画的第一页后，自动生成 Komga 风格系列封面
- 可按 Library 手动为更新时间最新的 N 个或全库 Series 批量生成封面
- 可按 Series / Book 手动生成或强制重建系列封面和归档索引
- 记录归档索引耗时、系列封面生成耗时和真实 Range 下载平均速度
- 管理页按 Library 和全部 Library 汇总漫画归档文件容量
- 管理页面：账号、根目录、扫描与封面任务进度、自动扫描、缓存统计和清理
- Windows 双击启动后自动打开管理页，并自动选择用户数据目录
- GitHub Release 提供 Windows、Linux 和 macOS 的直接运行包及 SHA256 校验和
- Docker Hub 提供 Linux amd64 / arm64 多架构镜像
- 整个服务免认证

## 尚不支持

- 固实 RAR、加密 RAR、分卷 RAR、7z、加密 ZIP
- 115 文件上传、删除、移动或重命名
- 真实 Komga 数据库兼容
- 多用户权限
- Mihon 原版 Komga 扩展的页级阅读进度同步
- OPDS、Web Reader、Bangumi 元数据
- downurl 不支持 HTTP Range 时自动下载整包

## 支持的平台

- Windows x64
- Linux x64 / ARM64
- Docker Linux x64 / ARM64
- macOS Intel / Apple Silicon
- 115 Open `refresh_token`
- Koharia
- 支持 Komga 扩展的 Mihon

## 快速开始

<p align="center">
  <img src="docs/images/quick-start.png" alt="下载、授权、添加漫画目录并通过局域网连接 Mihon" width="920">
</p>

### Windows：下载后双击

1. 打开 [Releases](https://github.com/xJogger/fake-komga-115/releases/latest)，下载
   `windows_amd64.zip`。
2. 解压整个压缩包，然后双击 `fake-komga-115.exe`。
3. 保持控制台窗口运行；默认浏览器会自动打开管理页。关闭网页不会停止服务，
   关闭控制台窗口才会停止服务。
4. Windows 防火墙首次询问时，仅允许“专用网络”访问。

Windows 默认把数据库、缓存和封面保存到：

```text
%LOCALAPPDATA%\fake-komga-115\data
```

管理页会显示实际数据存储路径，以及可在 Koharia / Mihon 中填写的局域网地址。发布包未进行
代码签名，Windows SmartScreen 可能显示未知发布者提示。

### Linux / macOS：直接运行发布包

从 [Releases](https://github.com/xJogger/fake-komga-115/releases/latest) 下载对应
平台压缩包，解压后运行：

```bash
chmod +x fake-komga-115
./fake-komga-115
```

Linux 和 macOS 默认不会自动打开浏览器，数据目录仍为当前目录下的 `./data`。
打开 `http://127.0.0.1:25600/admin` 完成配置。macOS 发布包同样没有代码签名。

### Docker：服务器部署

Docker Hub 镜像支持 `linux/amd64` 和 `linux/arm64`。推荐使用随仓库提供的
`docker-compose.yml`，它会创建命名卷保存数据库、缓存和封面：

```bash
mkdir fake-komga-115
cd fake-komga-115
curl -fsSLO https://raw.githubusercontent.com/xJogger/fake-komga-115/main/docker-compose.yml
docker compose up -d
```

然后打开：

```text
http://服务器地址:25600/admin
```

也可以直接运行：

```bash
docker run -d \
  --name fake-komga-115 \
  --restart unless-stopped \
  -p 25600:25600 \
  -v fake-komga-115-data:/data \
  -e TZ=Asia/Shanghai \
  blacktal/fake-komga-115:latest
```

容器内默认参数等价于：

```text
FK115_HOST=0.0.0.0
FK115_PORT=25600
FK115_DATA_DIR=/data
FK115_OPEN_BROWSER=false
```

容器以非 root 用户 `10001:10001` 运行。使用命名卷时通常不需要处理权限；如果改用
宿主机目录绑定挂载，例如 `./data:/data`，遇到权限错误时需要让该目录可由 UID 10001
写入。Docker 部署同样没有认证功能，只适合可信局域网。

### 从源码运行

需要 Go 1.26+：

```bash
git clone https://github.com/xJogger/fake-komga-115.git
cd fake-komga-115
go run ./cmd/server
```

默认参数：

```text
监听地址：0.0.0.0:25600
数据目录：./data
数据库：  ./data/fake-komga-115.db
缓存：    ./data/cache
```

也可以指定：

```bash
go run ./cmd/server \
  --host 0.0.0.0 \
  --port 25600 \
  --data-dir ./data
```

对应环境变量：

```text
FK115_HOST
FK115_PORT
FK115_DATA_DIR
FK115_OPEN_BROWSER
```

构建：

```bash
go build -o fake-komga-115 ./cmd/server
./fake-komga-115
```

项目也提供了 Makefile：

```bash
make test
make build
make run
```

### 启动参数

```text
--host           监听地址，默认 0.0.0.0
--port           监听端口，默认 25600
--data-dir       数据目录；Windows 默认为 %LOCALAPPDATA%\fake-komga-115\data，
                 其他平台默认为 ./data
--open-browser   启动成功后打开管理页；Windows 默认 true，其他平台默认 false
--version        显示版本
```

Linux 原有部署方式不受影响。若 systemd 已显式传入 `--data-dir`，升级后继续使用
原目录，也不会自动打开浏览器。

## 初始化与授权

1. 启动服务。
2. 打开 `http://服务器地址:25600/admin`。
3. 打开 <https://api.oplist.org/>。
4. 选择“115网盘 OAuth2 授权登录”，勾选“使用 OpenList 提供的参数”。
5. 使用 115 账号登录并完成授权。
6. 将页面返回的 **Refresh Token** 和 **Access Token** 复制到管理页的同名字段。
7. 点击“保存并验证”。
8. 添加一个或多个 115 漫画根目录 CID。
9. 点击“扫描全部”或单个 Library 的“扫描”按钮，等待完成。

`api.oplist.org` 是外部授权工具。Token 只会由本服务保存到本机数据目录中的
SQLite；不要把 Token 发给他人，也不要截图公开。

扫描默认不会自动运行。管理页可以分别启用：

- 定时自动扫描
- 服务启动时扫描

扫描成功后才会删除数据库中已经不存在的 Series/Book。扫描失败或取消时，上一次
完整数据会被保留。

管理页显示的漫画容量是已识别为 Book 的 CBZ/ZIP/CBR/RAR 文件大小之和，不包含
目录中的其他普通文件，也不会为了统计容量读取漫画内容。

Koharia / Mihon 中每卷漫画的日期使用扫描时已经保存的 115 文件创建时间，而不是
首次扫库时间；升级到该行为不需要重新扫描，只需在客户端中刷新章节列表。

Koharia / Mihon 的“添加时间”使用 Series 中最新一本漫画的 115 文件创建时间排序，
“更新时间”使用最新一本漫画的 115 文件修改时间排序，均支持最新在前和最旧在前。

## 目录映射

假设根目录为：

```text
漫画/
  作品 A/
    第001话.cbz
    第002话.cbr
  分类目录/
    作品 B/
      Vol.01.zip
```

映射结果：

```text
作品 A → Series
作品 B → Series
分类目录 → 不生成 Series
```

如果一个目录同时包含漫画文件和子目录，该目录和符合条件的子目录都会各自生成
Series。Series 显示名称始终使用文件夹名。

Library 启用 One-Shots 后会忽略上述目录映射，递归扫描根目录内所有支持的漫画
归档文件。每个文件生成一个独立 Series，Series 名称为去掉扩展名的文件名，Book
名称保留原始文件名。切换模式后需要重新扫描；转换数据先暂存，只有扫描成功才会
替换旧结构，失败、取消或服务重启不会删除上一次完整结构。

## 推荐客户端与配置

推荐客户端：

- **Koharia**：推荐优先尝试。使用内置 Komga 源连接本服务，可使用 Library 筛选、
  书库分类、结构化搜索和 Book 页级进度同步。
- **Mihon + Komga 扩展**：兼容稳定，适合只需要基础浏览、阅读和 Komga Tracker
  卷级同步的场景。

在 Koharia 或 Mihon 的 Komga 源中设置：

```text
Address:  http://服务器地址:25600
Username: 留空
Password: 留空
API Key:  留空
```

服务整体免认证，仅适合可信局域网。不要直接暴露到公网。

如果服务器有多个网卡，管理页会列出检测到的局域网地址。选择与运行客户端的手机
处于同一局域网、且能从手机访问的地址。

## WebView 信息页

Koharia / Mihon 漫画条目和阅读界面的 WebView 按钮会打开本服务的本地信息页：

- `/series/{seriesId}`：系列封面、所属 Library、115 相对路径、容量、时间、
  阅读/索引/下载统计及 Book 列表
- `/book/{bookId}` 和 `/books/{bookId}`：Book 文件路径、大小、页数、格式、时间、
  索引状态和真实下载速度

Book 列表可以继续进入对应 Book 信息页。普通 Book 页面不显示系列封面；
One-Shots Book 可以复用已经存在的系列封面。打开信息页只读取 SQLite 和已有封面，
不会请求 115 或为了封面读取漫画第一页。管理页和信息页均跟随系统深浅色主题。

## 阅读进度

如果在 Mihon 中启用 Komga Tracker，本服务会按 Mihon 的
`lastBookNumberSortRead` 实现标准卷级进度同步：读完某个 Book 后，Mihon 会把已读到
的卷号同步给服务端；服务端严格以 Mihon 发来的值为准，允许进度前进或回退。Mihon
原版 Komga 扩展的同步只到 Book / Chapter 级，不包含页码。

Koharia 会通过 Komga Book 进度接口把当前页码同步到服务端。本服务实现
`PATCH /api/v1/books/{bookId}/read-progress`，并在 Book DTO 中返回
`readProgress.completed`、`readProgress.page` 和 `readProgress.readDate`。Koharia 的
`read_status` 筛选严格以这份真实同步进度为准：

- 没有记录：`UNREAD`
- 有页码但未完成：`IN_PROGRESS`
- `completed=true`：`READ`

服务端还会在页面图片成功返回后记录推断页码，包括最近加载页、该 Book 的最大加载
页、总页数和更新时间，并在 Series / Book 信息页展示。这个页码来自图片加载请求，
可能包含 Mihon / Koharia 的预读页面，因此只作为“推断进度”显示，不会影响 Tracker
或 Koharia 页级同步的真实已读状态，也不会自动把 Book 标记为已读。

## 手动维护与性能统计

Series 信息页提供：

- 生成系列封面
- 强制重建系列封面
- 为该 Series 下全部 Book 生成归档索引
- 强制重建该 Series 下全部 Book 的归档索引

Book 信息页提供当前这一本的归档索引生成和强制重建。默认操作会跳过已有且仍匹配
当前文件版本的封面或索引；强制重建按钮带警示样式和二次确认。Series 级任务在后台
串行执行，可以在详情页查看进度并请求取消。取消不会强行中断当前正在处理的那一本，
而是在当前条目结束后尽快停止后续条目。

详情页会展示当前 Series / Book 的索引耗时、封面生成耗时和真实下载平均速度。
管理页的服务状态区域会汇总全局平均索引耗时、平均封面耗时和真实 Range 下载平均
速度。下载速度只统计真正从 115 downurl 成功读取的 HTTP Range 字节；缓存命中、
等待并发合并结果和失败请求不计入。

## 缓存

管理页可以分别查看和清理：

- `range`：115 文件的远程字节块
- `page`：已解压的 ZIP deflate 或 RAR 页面
- `archive`：ZIP/RAR 页面名称、大小和读取位置索引
- `thumbnail`：从 Series 第一本漫画第一页生成的压缩封面

默认限制：

```text
Range block: 1 MiB
RAR 索引 block: 64 KiB
RAR 最大解压字典: 100 MiB
Range cache: 10 GiB
Page cache: 5 GiB
单页上限: 100 MiB
翻页预读: 2 页
下一卷索引预读: 默认关闭，启用后的触发阈值为剩余 10 页
```

Range 和 Page 缓存上限可以在管理页修改，设置为 `0` 表示不限制。降低上限并保存
后会立即淘汰最久未使用的缓存。Range、Page、归档索引和系列封面均可独立清理。

管理页还可以启用“下一卷索引预读”。当前页面成功返回后，如果当前卷剩余页数
小于或等于设定阈值，服务会在后台只建立同一 Series 中自然排序下一本 Book 的
ZIP/RAR 索引，不读取、解压或缓存下一卷页面。默认关闭，阈值默认 10 页，可设置
为 1–100。任务全局串行并合并重复请求；One-Shots、只有一本 Book 的 Series 和
Series 最后一卷不会触发，失败也不会影响当前阅读。

系列封面使用 JPEG，最长边为 Komga 默认的 300px，不会放大小于该尺寸的原图。
封面文件保存在 `data/thumbnails/series`，元数据保存在 SQLite。第一本漫画发生
变化后旧封面会自动失效；JPEG、PNG、GIF 和 WebP 首页可以生成封面。

每个 Library 还提供两个手动封面按钮：

- 获取更新时间最新的 N 个 Series 封面，N 默认是 50，可在按钮左侧修改
- 获取整个 Library 的全部 Series 封面

任务按 Series 逐个执行，全局同时只运行一个封面任务。每个 Series 只读取自然排序
第一本漫画的第一页；已有且来源仍有效的封面直接跳过。单个 Series 失败不会中断
后续任务，管理页会保存进度、生成/跳过/失败数量和有限的错误摘要，也可以取消
排队中或运行中的任务。两个启动按钮均为警示按钮，并要求二次确认；全库任务可能
产生大量 115 流量和缓存数据。

缓存清理不会修改 115 文件，也不会删除扫描得到的 Library、Series 或 Book。

## 数据和安全

- Token 明文保存在本地 SQLite 中。
- 数据库文件权限设置为 `0600`。
- Token、Authorization、API Key 和完整 downurl 不写日志。
- 管理 API 和 Komga API 均无认证。
- 服务默认绑定 `0.0.0.0`，请确保只在可信局域网中访问。
- 建议定期备份 `data/fake-komga-115.db`。

## 测试

```bash
go test ./...
```

发布工作流还会在推送 `v*` Tag 时构建：

- Windows amd64 ZIP
- Linux amd64 / arm64 tar.gz
- macOS amd64 / arm64 tar.gz
- `SHA256SUMS`

所有二进制产物由 GitHub Actions 自动放入对应 GitHub Release。发布 `vX.Y.Z`
前必须提交 `docs/release-notes/vX.Y.Z.md`，Release 页面会使用该文件作为更新说明；
文件缺失时发布工作流会失败。另一个 Docker 工作流会在同一个 Tag 上构建并推送
`blacktal/fake-komga-115` 的 `linux/amd64,linux/arm64` 多架构镜像，包含 `latest`、
`vX.Y.Z`、`X.Y.Z` 和 `X.Y` 标签。

测试包括自然排序、ID、MIME 类型，以及用 mock HTTP Range 服务读取远程
store/deflate ZIP、RAR4 和压缩 RAR5 页面。

## 常见问题

### 会在启动时下载整个漫画库吗？

不会。启动时只加载 SQLite 和设置。扫描也只调用 115 文件列表 API。

### 第一次扫描为什么很久？

为了准确统计所有 Series 和 Book，需要递归列出每个目录。默认限制为每秒一次
115 API 请求；上万个目录可能需要数小时。扫描进度会持久化显示，且不会读取漫画
文件内容。

### 打开漫画时为什么第一次比较慢？

第一次打开需要通过 HTTP Range 读取 ZIP 尾部和 central directory，或遍历 RAR
文件头。索引会保存到 SQLite，后续打开直接复用。

RAR 页面不会整包下载：服务定位目标 entry 后通过 Range 读取其压缩数据，单页在
内存中完成解压并写入页面缓存。固实、加密或分卷 RAR 会返回明确的 `422` 错误。

### downurl 不支持 Range 怎么办？

服务返回 `501 RANGE_NOT_SUPPORTED`，不会偷偷下载整本漫画。

### 可以公网部署吗？

当前版本整个服务免认证，不应直接公网部署。

## 后台运行

可以使用 systemd 用户服务。请将下面的路径替换为自己的实际路径：

```ini
[Unit]
Description=fake-komga-115
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/path/to/fake-komga-115
ExecStart=/path/to/fake-komga-115/fake-komga-115 --host 0.0.0.0 --port 25600 --data-dir /path/to/fake-komga-115/data
Restart=on-failure
RestartSec=3
UMask=0077

[Install]
WantedBy=default.target
```

保存为 `~/.config/systemd/user/fake-komga-115.service` 后执行：

```bash
systemctl --user daemon-reload
systemctl --user enable --now fake-komga-115
systemctl --user status fake-komga-115
```

## 升级与备份

升级前建议停止服务并备份 `data/fake-komga-115.db`。数据库迁移会在启动时自动执行：

```bash
systemctl --user stop fake-komga-115
cp data/fake-komga-115.db data/fake-komga-115.db.backup
git pull
go build -o fake-komga-115 ./cmd/server
systemctl --user start fake-komga-115
```

Docker Compose 部署可以这样升级：

```bash
docker compose pull
docker compose up -d
```

## 二次开发

- [AGENTS.md](AGENTS.md)：给 AI 编程助手和贡献者的开发约束与修改清单。
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)：架构、数据模型、扫描事务、
  远程归档读取和兼容 API 的技术说明。
- [SECURITY.md](SECURITY.md)：安全边界和漏洞报告方式。

## 参考

- [115 Open API](https://www.yuque.com/115yun/open)
- [OpenList 115 Open 驱动](https://github.com/OpenListTeam/OpenList/tree/main/drivers/115_open)
- [Komga API](https://komga.org/docs/openapi/komga-api/)
- [Koharia](https://github.com/Mister-album/Koharia)
- [Mihon Komga 扩展](https://github.com/keiyoushi/extensions-source/tree/main/src/all/komga)
- [rardecode](https://github.com/nwaples/rardecode)

## License

[MIT](LICENSE)
