# Security

## 安全边界

本项目定位为可信局域网内的单用户服务：

- 管理页面、管理 API 和 Komga 兼容 API 均不要求认证。
- 服务默认监听 `0.0.0.0:25600`。
- 115 Access Token、Refresh Token 和短期 downurl 会明文存入本地 SQLite。
- 数据库和缓存目录会尽量使用仅当前用户可读写的文件权限，但这不能替代主机隔离。

不要把服务直接暴露到互联网。需要跨网络访问时，应自行使用 VPN，或在反向代理
中配置 TLS、强认证、访问控制和日志脱敏。

## 敏感文件

以下内容不得提交到 Git：

- `data/` 及其中的数据库、缓存、封面和日志
- `user_info*`
- `.env*`、本地配置文件、Access Token、Refresh Token
- 真实 downurl、Cookie、Authorization Header
- 包含私人漫画目录名或账号信息的调试输出

## 报告漏洞

请不要在公开 Issue 中提交可用 Token、真实 downurl 或其他个人信息。可以先创建
不含敏感数据的最小复现；如果问题必须包含隐私信息，请通过 GitHub 仓库所有者的
私下联系方式报告。
