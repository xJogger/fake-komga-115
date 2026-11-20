# 发版流程

1. 在开发分支准备 `docs/release-notes/vX.Y.Z.md`，突出不兼容改动与升级步骤。核对 README、程序版本、Docker 示例、许可文件和跨平台发布包；不要提交私有数据或配置。
2. 开发分支 CI 通过后合入 `main`，再确认目标提交的 CI 通过。若 `main` 未分叉，可用 `git merge --ff-only`，不要强推或重写历史。
3. 为已经推送并通过 CI 的提交创建并推送 `vX.Y.Z` 注解标签。标签触发 CI、GitHub Release 和 Docker。Release 工作流打包五个平台的二进制、签名 ARM64 APK、文档和 `SHA256SUMS`；Android 使用限分支和标签的环境自动签名，Release 等签名工件就绪后才创建；Docker 工作流只推版本标签，不改 `latest`。
4. 等工作流完成，下载 GitHub 产物并核对 SHA256、压缩包内容和版本，确认 APK 的包名和签名证书与已验收的预览一致，检查 Docker 版本标签的清单包含 `linux/amd64` 和 `linux/arm64`。记录已验证的镜像索引摘要。
5. 如果该版本应为 Docker 的默认版本，在 `main` 手动运行 `Promote Docker latest`，填入稳定版本标签和已验证的索引摘要。工作流会核对 GitHub 发布及附件、镜像摘要与两种架构，再把同一镜像索引关联到 `latest`；不会重新构建镜像。最后确认 `latest` 与版本标签指向同一摘要。

```bash
docker buildx imagetools inspect "blacktal/fake-komga-115:$VERSION"
gh workflow run docker-promote.yml --ref main -f version="$VERSION" -f digest="$VERIFIED_DIGEST"
```

其中 `VERSION` 为 `vX.Y.Z`，`VERIFIED_DIGEST` 为核对后的 `sha256:…` 索引摘要。

从 `v2.2.0` 起，GitHub Release 服务端归档与二进制名统一为 `mangaalpha_vX.Y.Z_<os>_<arch>` / `mangaalpha`（`mangaalpha.exe`）；`Promote Docker latest` 同时兼容新命名与历史 `fake-komga-115_vX.Y.Z_*` 归档以便回退。Docker 镜像名 `blacktal/fake-komga-115`、容器入口、`FK115_*` 环境变量与既有默认数据路径保持不变。

发布和部署是两件事。验证发布产物后再升级运行中的服务；重大版本升级前先备份原数据目录。Docker 用户可通过固定版本标签选择何时升级。
