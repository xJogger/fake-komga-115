# fake-komga-115

[![CI](https://github.com/xJogger/fake-komga-115/actions/workflows/test.yml/badge.svg)](https://github.com/xJogger/fake-komga-115/actions/workflows/test.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

`fake-komga-115` 是一个面向自用场景的 Komga 兼容服务。Mihon 使用 Komga
扩展连接它，而漫画目录和 CBZ/ZIP/CBR/RAR 文件实际来自 115 Open API。

```text
Mihon → fake-komga-115 → 115 Open API → downurl → HTTP Range → ZIP/RAR 页面
```

它不会运行真实 Komga、挂载 115 网盘或启动时下载漫画文件。扫描只读取目录和
文件元数据；只有用户打开一本漫画时才读取 ZIP central directory 或 RAR 文件头，
翻页时才读取并解压对应图片所需的远程字节范围。

> [!WARNING]
> 当前版本是 `v0.1.0` 开发预览版。服务没有认证功能，115 Token 会明文保存在
> 本地 SQLite 中。仅应部署在可信局域网，不要直接暴露到公网。

## 当前功能

- 单个 115 Open 账号
- 多个 115 根目录，每个根目录映射为一个 Komga Library
- 递归扫描上万目录、十万级文件
- 一个含直接 CBZ/ZIP/CBR/RAR 文件的文件夹映射为一个 Series
- 只有子目录、没有直接漫画文件的目录不生成 Series
- Library 可切换 One-Shots 模式，递归把每个漫画归档文件映射为独立 Series
- 最新 Mihon Komga 扩展所需的 Series、Book、Pages、筛选和缩略图端点
- 远程 ZIP central directory 解析
- ZIP `store` 和 `deflate` 页面读取
- RAR4/RAR5 非固实、非加密、单卷页面读取
- Range block、归档索引、解压页面缓存
- 阅读一个 Series 第一本漫画的第一页后，自动生成 Komga 风格系列封面
- 管理页按 Library 和全部 Library 汇总漫画归档文件容量
- 管理页面：账号、根目录、扫描进度、自动扫描、缓存统计和清理
- 整个服务免认证

## 尚不支持

- 固实 RAR、加密 RAR、分卷 RAR、7z、加密 ZIP
- 115 文件上传、删除、移动或重命名
- 真实 Komga 数据库兼容
- 多用户权限
- 阅读进度跨设备同步
- OPDS、Web Reader、Bangumi 元数据
- downurl 不支持 HTTP Range 时自动下载整包

## 环境

- Linux
- Go 1.26+
- 115 Open `refresh_token`
- 支持 Komga 扩展的 Mihon

## 快速开始

克隆并启动：

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

## 初始化

1. 启动服务。
2. 打开 `http://服务器地址:25600/admin`。
3. 填入 115 Open `refresh_token`；`access_token` 可以留空。
4. 点击“保存并测试”。
5. 添加一个或多个 115 漫画根目录 CID。
6. 点击“扫描全部”或单个 Library 的“扫描”按钮。
7. 等待扫描完成。

扫描默认不会自动运行。管理页可以分别启用：

- 定时自动扫描
- 服务启动时扫描

扫描成功后才会删除数据库中已经不存在的 Series/Book。扫描失败或取消时，上一次
完整数据会被保留。

管理页显示的漫画容量是已识别为 Book 的 CBZ/ZIP/CBR/RAR 文件大小之和，不包含
目录中的其他普通文件，也不会为了统计容量读取漫画内容。

Mihon 中每卷漫画的日期使用扫描时已经保存的 115 文件创建时间，而不是首次扫库
时间；升级到该行为不需要重新扫描，只需在 Mihon 中刷新章节列表。

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

## Mihon 配置

在 Mihon 中安装 Komga 扩展，然后设置：

```text
Address:  http://服务器地址:25600
Username: 留空
Password: 留空
API Key:  留空
```

服务整体免认证，仅适合可信局域网。不要直接暴露到公网。

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
```

Range 和 Page 缓存上限可以在管理页修改，设置为 `0` 表示不限制。降低上限并保存
后会立即淘汰最久未使用的缓存。Range、Page、归档索引和系列封面均可独立清理。

系列封面使用 JPEG，最长边为 Komga 默认的 300px，不会放大小于该尺寸的原图。
封面文件保存在 `data/thumbnails/series`，元数据保存在 SQLite。第一本漫画发生
变化后旧封面会自动失效；JPEG、PNG、GIF 和 WebP 首页可以生成封面。

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

## 二次开发

- [AGENTS.md](AGENTS.md)：给 AI 编程助手和贡献者的开发约束与修改清单。
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)：架构、数据模型、扫描事务、
  远程归档读取和兼容 API 的技术说明。
- [SECURITY.md](SECURITY.md)：安全边界和漏洞报告方式。

## 参考

- [115 Open API](https://www.yuque.com/115yun/open)
- [OpenList 115 Open 驱动](https://github.com/OpenListTeam/OpenList/tree/main/drivers/115_open)
- [Komga API](https://komga.org/docs/openapi/komga-api/)
- [Mihon Komga 扩展](https://github.com/keiyoushi/extensions-source/tree/main/src/all/komga)
- [rardecode](https://github.com/nwaples/rardecode)

## License

[MIT](LICENSE)
