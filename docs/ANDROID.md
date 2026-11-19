# Android 独立预览版

这是可独立运行的 Android 应用，不依赖家里的服务端，不同步书库或阅读进度，也不是 Mihon 插件。应用内通过 WebView 使用同一套网页书架、管理界面和阅读器。

## 下载与安装

1. 正式版从 [GitHub Releases](https://github.com/xJogger/MangaAlpha/releases) 下载 `mangaalpha-arm64.apk`。签名预览版则从 **Android APK preview** 的 `mangaalpha-android-signed` 工件解压取得；不要下载或尝试安装 unsigned 工件。
2. 安装目标为 Android 8.0+ 的 ARM64 手机。MangaAlpha 使用 `io.github.xjogger.mangaalpha`，与旧的 Fake Komga 115 调试版是两个独立应用，不共用数据。今后同包名的正式签名包可以覆盖更新（无需卸载）；卸载会删除该应用的数据库、缓存和设置。
3. 第一次打开先设管理员。可以添加 115、WebDAV，或在「管理后台 → 书库 → 本地」点「选择手机目录」并允许读取。应用只保存系统授权的 URI，无需全盘权限或 Shizuku。权限撤销后，用系统选择器重新选择同一个目录即可重新授权；不必删除书库。

## 运行约束和验证清单

- 手机书库是只读的。扫描仅读目录、文件大小与时间；读取时通过系统文件描述符随机访问。不能随机访问的文件提供方不会触发整包复制，请改用支持随机读的本机目录或 WebDAV。
- 数据库、封面、Range 和页面缓存各自保存在应用私有目录，与服务器部署完全独立。SQLite 临时文件也写入应用私有缓存目录，不移动主数据库。请按手机余量调整缓存上限。锁屏、离开界面时可借助前台服务继续任务；从通知或管理后台可手动停止，也可在系统应用设置中停止。系统或设备厂商仍可能结束进程；不随开机启动。
- APK 内的 Go 服务只监听设备回环地址，另有应用内部凭据防止其他本机应用直接访问，不用它向局域网或外部漫画客户端提供服务。
- 安装试用时至少检查：首次设密、选目录并扫描、重启后目录授权仍有效、撤销权限后不会清空旧书库、从本地/115/WebDAV 各打开一卷、切到后台和通知停止、系统 16 KB 内存页设备能启动。CI 会校验签名、APK ZIP 对齐与 ARM64 Go 库 ELF 对齐，但不代替这些真机验证。

## 开发与签名

推送到 `feat/v2.1` 触发签名预览；正式版由 Release 工作流调用同一打包流程，共用固定的签名证书。先构建无签名 APK，再由使用 `android-signing` 环境的独立任务自动签名。只允许可信维护者推送该环境允许的开发分支和发布标签，不把签名密钥设为全仓库密钥。环境变量 `ANDROID_SIGNING_CERT_SHA256` 是公开证书摘要，`ANDROID_SIGNING_KEYSTORE_B64` 和 `ANDROID_SIGNING_STOREPASS` 是仅签名任务可访问的环境密钥。签名库与密码必须在 Git 仓库之外保留备份；更新时不要重新生成私钥。预览的版本代码低于同版本正式包。

构建顺序见 [Android 打包流程](../.github/workflows/android-package.yml)：构建 `/reader` 静态包，使用 Android NDK + Go `gomobile bind` 生成 `android/app/libs/fk115.aar`，再由 Gradle 打包。`mobile/bridge.go` 持有应用实例；`android/` 负责系统目录授权、生命周期与 WebView；`internal/localfs` 中的 Android 适配只在嵌入服务启动后注册。服务器二进制与镜像仍使用 `CGO_ENABLED=0`，不需要 Android 工具链。
