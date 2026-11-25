## autobro

自动化控制浏览器完成任务

`make newsl` 和 `make newslv` 一次创建 3 个任务。定时脚本每 6 小时运行一次，按 UU → UU → UU → SL 循环，每轮尝试创建 1 个任务，随后执行 `make relog N=2`。SL 累计 3 次尝试后，在下个 SL 轮次换号。

定时 UU 轮次先确保 `.env.local` 中只有一个当前可用账号。没有账号、会员到期或配额耗尽时执行 `make reguu ARGS=-ensure` 注册新账号，验证会员和配额后原子替换旧账号，再创建 ChatGPT 账号。通过 `make reguu ARGS=-prepare` 配合 `make useuu UU_EMAIL=邮箱` 仍可手动准备和切换账号。配置错误或临时查询失败不会触发换号。

`make relog` 按本地账号顺序向 SceneMint 批量查询，每批最多 100 个邮箱。SceneMint 只返回凭据失效、过期或记录缺失的账号，管理员停用的账号会被跳过。

`make relog N=2` 每轮最多尝试登录两次，成功和失败都计入上限。达到上限时，下次从最后一次尝试后的账号继续扫描。批量查询失败时，下次从该批起点重试。`relog-cursor` 记录续扫位置。不指定 `N` 时从头扫描一圈，尝试次数不限。
