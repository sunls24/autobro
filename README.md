## autobro

自动化控制浏览器完成任务

`make newsl` 和 `make newslv` 一次创建 3 个任务。定时脚本每 6 小时运行一次，按 UU → UU → UU → SL 循环，每轮尝试创建 1 个任务，随后执行 `make relog N=2`。SL 累计 3 次尝试后，在下个 SL 轮次换号。

定时 UU 轮次先确保 `.env.local` 中只有一个当前可用账号。没有账号、会员到期或配额耗尽时执行 `make reguu ARGS=-ensure` 注册新账号，验证会员和配额后原子替换旧账号，再创建 ChatGPT 账号。通过 `make reguu ARGS=-prepare` 配合 `make useuu UU_EMAIL=邮箱` 仍可手动准备和切换账号。配置错误或临时查询失败不会触发换号。

`make relog` 使用本地 `relog-checks.json` 跳过冷却中的账号：成功上传令牌后冷却 12 小时；查询确认账号有效后，冷却不超过令牌过期时间。提前失效可能要到下一次查询才发现；删除缓存可让下轮重新查询。

`make relog N=2` 达到两次登录尝试后，若还有未扫描账号，会在本地 `relog-cursor` 中记录下次扫描起点。不指定 `N` 时仍从头扫描，不使用游标。
