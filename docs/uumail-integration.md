# Uumail (uu.me) 邮箱提供商接入

2026-09-16 完成接入并端到端验证。使用模式与 SimpleLogin 完全对齐：

```bash
make reguu                # 注册 1 个 Uumail 账号（SunMail 地址作身份），自动更新 .env.local
make newuu ARGS="-c 4"    # 用 uu provider 批量注册 ChatGPT 账号（不带 ARGS 默认 -c 4）
```

## 架构

```
cmd/uumailregister ──注册──▶ UUMAIL_ACCOUNTS(.env.local) + uumail_sessions.json(会话缓存)
                                    │
provider "uu"(internal/mail/uumail.go) ──懒登录/建别名──▶ api.uu.me
                                    │
mail.From(uu, SunMail) ──取码──▶ ChatGPT 注册流程（浏览器/协议，零改动）
```

- Uumail 账号身份 = 一个 SunMail 地址（`chato.eu.org` 域）；别名 `{prefix}@{username}.uu.me`
  的邮件转发回该 SunMail 地址，取码沿用现有 SunMail `IMailWait`。
- 登录是纯 HTTP 邮箱验证码链路（`commonapi.mxfast.com` send-code → verify-code → OTT
  → `api.uu.me/v1/user/login`），cookie `uumail_ut` 有效期 30 天，同一邮箱可随时重登续期
  （服务端 60s 频控，代码内等待 65s 重试一次）。
- 会话缓存 `uumail_sessions.json`（0600，已 gitignore）：跨进程合并写入（独占锁 + 临时文件
  rename 原子替换），cookie 过期自动重登。
- 别名配额从 `user/info` 的 `limits.aliasLimits` 动态读取；配额耗尽自动轮换下一账号。
- 注册流程把 `mail_provider=uu` 写入 accounts.jsonl（`-m uu` 时默认 `-s` 开启）。

## 代码位置

| 内容 | 文件 |
| --- | --- |
| provider + 客户端 + 会话缓存 | `internal/mail/uumail.go` |
| provider 工厂（`uu`） | `internal/mail/provider.go` |
| 账号注册编排 + env 原子更新 | `internal/uumailregister/` |
| 注册命令入口 | `cmd/uumailregister/main.go` |
| toapi 接线（flag/config/storeAccounts） | `cmd/toapi/main.go`、`internal/toapi/config.go` |
| make 目标 | `Makefile`（reguu / reguuv / newuu） |

## 账号资产

账号邮箱、用户名、UID、试用期限及其他运行数据属于本地资产，不应提交到仓库。运行 `make reguu` 后会自动写入本地 `.env.local` 和 `uumail_sessions.json`。

## 测试与验证记录

- `go build ./... && go vet ./... && go test -race ./...` 全绿。
- 单测覆盖：登录链路（含非 JSON 401 归一化、频控重试路径）、过期会话自动重登、
  别名配额轮换/耗尽、创建冲突重试、会话缓存持久化/权限收紧/跨进程合并/并发写入、
  成功响应携带 message 字段不误判、错误响应体截断。
- 真实链路：`make reguu` 真实注册账号 → `make newuu -c 1` 两次完整 ChatGPT 注册成功；
  live 冒烟测试（`UUMAIL_LIVE=1 go test -run TestUumailLiveSmoke ./internal/mail/`）覆盖
  真实登录（OTP 收码）→ 建别名 → 转发解析 → 删除，与 toapi 共用仓库根会话文件。
- codex code review 三轮：首轮 6 个 P2 全部修复（权限收紧、先监听后发码、401 归一化、
  索引加锁、锁+原子写缓存、过期重试）；第二轮自查修复 5 项，含 live 冒烟抓到的
  **真实契约 bug：`/v1/addr/delete` 只接受 alias 前缀（@ 前本地部分），传完整地址
  服务端报 invalidChars**——客户端已统一归一化，mock 测试同步复现服务端行为；
  终审 PASS。

## 已知事项 / 后置项

- Magic PIN 自动建别名未实测（本机出口 IP 被 mx3.uu.me 无 banner 断连），待手动验证。
- 免费版 3 别名限额未实测（测试号在 Plus 试用内），配额以服务端 `limits` 为准。
- 验证码邮件主题固定 `Login Verification Code`，6 位码在正文，SunMail 现有提取逻辑兼容。
- 会话缓存写失败仅告警不中断本次注册（设计决策：代价是下次多一次登录）。
- live 冒烟测试默认跳过；`UUMAIL_LIVE=1` 时执行，会产生并即时删除一个真实别名。
