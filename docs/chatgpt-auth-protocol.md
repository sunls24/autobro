# ChatGPT 注册认证协议实现方案

## 1. 目标

将当前 ChatGPT 注册/登录流程拆分为完整浏览器模式和协议混合模式。默认使用 Rod 完成完整浏览器流程；显式 `-p` 时由 HTTP 协议负责业务状态机，仅在 Sentinel runtime proof 或明确 Challenge 阶段使用 Rod。两个模式都按账号创建独立的临时浏览器 profile 和 Rod 会话，账号结束后关闭并清理。

本方案只处理当前项目需要的认证凭据获取：

```text
邮箱地址 -> ChatGPT OAuth 登录 -> 邮箱 OTP -> 账号资料 -> callback -> accessToken
```

登录成功后的 accessToken 仍由当前项目上传给 SceneMint。SceneMint 只作为已认证凭据的消费者和协议客户端实现参考，不属于注册认证协议的一部分。

## 2. HAR 证据与范围

当前全量 HAR 已确认以下链路：

```text
GET  chatgpt.com/
GET  chatgpt.com/auth/login_with
GET  chatgpt.com/api/auth/providers
GET  chatgpt.com/api/auth/csrf
POST chatgpt.com/api/auth/signin/openai
GET  auth.openai.com/api/accounts/authorize       302
GET  auth.openai.com/email-verification
GET  sentinel.openai.com/backend-api/sentinel/sdk.js
GET  sentinel.openai.com/sentinel/<version>/sdk.js
GET  sentinel.openai.com/backend-api/sentinel/frame.html
POST sentinel.openai.com/backend-api/sentinel/req         (email_otp_validate)
POST auth.openai.com/api/accounts/email-otp/resend
POST auth.openai.com/api/accounts/email-otp/validate
POST sentinel.openai.com/backend-api/sentinel/req         (oauth_create_account，可能有 SDK 预取/刷新)
POST sentinel.openai.com/backend-api/sentinel/req         (email_otp_validate，SDK 刷新)
POST auth.openai.com/api/accounts/create_account
POST sentinel.openai.com/backend-api/sentinel/req         (oauth_create_account，callback 前刷新)
GET  chatgpt.com/api/auth/callback/openai         302
GET  chatgpt.com/
```

HAR 中还包含大量静态资源、RUM、CES、第三方统计和实时连接。这些不作为认证主链路，除非协议请求实际返回 Challenge 或缺少必要状态时，再按失败证据补充。

HAR 导出不适合作为唯一证据：部分响应正文会被清理或没有落盘，且 Cookie、验证码、OAuth code/state、Sentinel payload 和 accessToken 都不能用于离线回放。

本次已在保留日志的 Chrome DevTools Network 面板中直接查看真实请求详情，补充确认了：

- `email-otp/validate` 的 JSON 请求体只有 `{"code":"<six-digit-code>"}`。
- `create_account` 的 JSON 请求体只有 `{"name":"<name>","birthdate":"YYYY-MM-DD"}`。
- 两个认证请求都带当前会话生成的 `Openai-Sentinel-Token` 和 `Openai-Sentinel-So-Token`，不能复制历史值。
- `create_account` 成功后会设置统一会话相关 Cookie，随后 callback 负责生成 ChatGPT 会话 Cookie。
- 已有账号复登录的实时 Network 记录已确认：入口使用 `screen_hint=login_or_signup`，验证码校验成功后直接返回/触发 ChatGPT callback，不经过 `create_account`；若响应包含 `account_deactivated`，协议层映射为 `ErrAccountDeactivated`，不能当作普通 OTP 错误重试。

因此，HAR 仅用于发现主链路；请求体、响应头和 Challenge 行为以实时 DevTools 观察及协议 fixture 为准，敏感值不进入代码。

## 3. 设计原则

1. 每个账号使用独立 `http.Client` 和 `cookiejar.Jar`。
2. 不复制完整浏览器 Cookie 字符串；只让 CookieJar 根据域、路径、Secure 和过期时间自动管理 Cookie。
3. 不硬编码 HAR 中的 OTP、OAuth code、state、CSRF、Session Cookie、Sentinel token 或 Challenge Cookie。
4. 只复制已确认的必要请求头；User-Agent、客户端版本和 build 号集中维护。
5. 所有状态迁移由显式状态机控制，未知页面、未知重定向和缺少必需字段直接报错。
6. OTP 校验和账号创建不做盲目自动重试，避免重复消费一次性凭据或重复创建账号。
7. 日志只记录阶段、路径、状态码、字段名和长度，不记录秘密值。

## 4. 协议状态机

### 4.1 初始化

为一次认证生成：

- `deviceID`：UUID，用于 `ext-oai-did` 和 OAuth 参数。
- `authSessionLoggingID`：UUID，用于登录 handoff 和 OAuth 参数。
- 每一次 Sentinel/认证调用需要的流程 ID、调用 ID和文档导航 ID由当前会话动态生成或从响应读取。

初始化请求：

```http
GET https://chatgpt.com/
POST https://chatgpt.com/unauth-mweb/auth/handoff
Content-Type: application/json

{"authSessionLoggingId":"...","authenticationStarted":true}
```

`events/*`、`conversation/prepare` 等匿名页面预热请求不进入第一版主链路。

### 4.2 ChatGPT OAuth 启动

```http
GET /auth/login_with?
    callback_path=/&
    screen_hint=signup&
    login_hint=<email>&
    auth_session_logging_id=<uuid>&
    ext-oai-did=<device-id>
```

随后获取 NextAuth 所需的 provider 和 CSRF：

```http
GET /api/auth/providers
GET /api/auth/csrf
```

CSRF 响应中的 `csrfToken` 和 Cookie 必须同时保留。

发起 OpenAI 登录：

```http
POST /api/auth/signin/openai?
    prompt=login&
    screen_hint=signup&
    ext-oai-did=<device-id>&
    auth_session_logging_id=<uuid>&
    login_hint=<email>
Content-Type: application/x-www-form-urlencoded

callbackUrl=https%3A%2F%2Fchatgpt.com%2F&csrfToken=<csrf>&json=true
```

响应中的授权 URL 不能自行猜测，必须解析服务端返回值。授权 URL 的 host 必须校验为 `auth.openai.com`，避免跟随意外外部地址。

### 4.3 OpenAI 授权页

手动处理 302：

```http
GET https://auth.openai.com/api/accounts/authorize?...
```

HAR 中该响应跳转到：

```text
/email-verification
```

协议客户端应保存授权页响应设置的 Cookie，并读取 `x-openai-document-navigation-id` 等后续请求需要的响应头。

### 4.4 邮箱验证码

初始 OTP 由授权页触发，客户端通过现有邮箱接口等待邮件。

重发请求：

```http
POST https://auth.openai.com/api/accounts/email-otp/resend
Referer: https://auth.openai.com/email-verification
```

验证码校验：

```http
POST https://auth.openai.com/api/accounts/email-otp/validate
Content-Type: application/json
Origin: https://auth.openai.com
Referer: https://auth.openai.com/email-verification
openai-sentinel-token: <current-token>
openai-sentinel-so-token: <current-token>
x-access-flow-invocation-id: <uuid>
x-openai-document-navigation-id: <response-header>

{"code":"<six-digit-code>"}
```

验证码只从邮箱接口实时读取一次，不写日志，不保存到账号文件。

### 4.5 账号资料

验证码成功后先请求并吸收 `/about-you` 页面状态，然后提交：

```http
GET https://auth.openai.com/about-you
```

随后提交：

```http
POST https://auth.openai.com/api/accounts/create_account
Content-Type: application/json
Origin: https://auth.openai.com
Referer: https://auth.openai.com/about-you
openai-sentinel-token: <current-token>
openai-sentinel-so-token: <current-token>
x-access-flow-invocation-id: <uuid>
x-openai-document-navigation-id: <response-header>

{"name":"<generated-name>","birthdate":"YYYY-MM-DD"}
```

账号创建响应设置的认证 Cookie 必须进入同一个 CookieJar。HAR 证明该响应之后会进入 ChatGPT callback。
当前前端的 next-step 响应包装还可能把 callback 放在 page.type=external_url、page.payload.url 中；协议解析器同时支持该结构和顶层 URL 字段，并继续校验 callback host、code、state。

### 4.6 OAuth callback 与凭据

```http
GET https://chatgpt.com/api/auth/callback/openai?
    code=<one-time-code>&
    scope=<scope>&
    state=<state>
```

callback 的 302 目标由服务端响应决定，不能硬编码为成功。响应会设置分片的 NextAuth session Cookie，例如：

```text
__Secure-next-auth.session-token.0
__Secure-next-auth.session-token.1
```

callback 完成后请求：

```http
GET https://chatgpt.com/api/auth/session/
```

响应中的 `accessToken` 必须为非空字符串，才算认证成功。`/backend-api/me` 只用于可选的账号确认，不作为 accessToken 的唯一来源。

### 4.7 已有账号 Renew

已有账号不申请新邮箱，也不提交资料。实时登录记录确认入口只需将 OAuth 参数切换为：

```http
GET /auth/login_with?...&screen_hint=login_or_signup&login_hint=<existing-email>
POST /api/auth/signin/openai?...&screen_hint=login_or_signup&login_hint=<existing-email>
```

之后仍复用 provider、CSRF、authorize、邮箱验证码和 Sentinel `email_otp_validate`。验证码校验成功后，响应中的 callback URL（顶层 URL、重定向字段或 `page.payload.url`）直接进入：

```text
email-otp/validate -> chatgpt.com/api/auth/callback/openai -> /api/auth/session/
```

该分支禁止调用 `create_account`，不会覆盖账号的邮箱元数据；如果响应表明账号已停用，则返回 `ErrAccountDeactivated`，由续期流程移除本地账号记录，不删除 SimpleLogin 别名。

## 5. Sentinel 与 Challenge

HAR 证明认证阶段存在多次：

```http
POST https://sentinel.openai.com/backend-api/sentinel/req
```

请求体包含：

```json
{"flow":"...","id":"<uuid>","p":"<generated-payload>"}
```

实时请求还确认了：

- `Content-Type` 为 `text/plain;charset=UTF-8`，但载荷本身是 JSON 对象。
- `id` 使用当前会话的 `oai-did`，不是可以跨账号复用的固定 ID。
- `flow` 随认证阶段变化；注册 HAR 中确认了 `email_otp_validate` 和 `oauth_create_account`，两者各出现两次，后一次属于 SDK 预取/刷新时机，不能简单按次数重放。另一次复登录现场还观察到邮箱验证页加载后的 `authorize_continue` 前置请求；它不能据此写入注册主链路，也不能把它误用于 `create_account`。
- `p` 是 Sentinel SDK 根据当前浏览器环境生成的受保护字符串，长度、时间、UA 和运行时探针都会变化。

`p` 是当前页面运行时生成的受保护数据，不能使用 HAR 的历史值。响应还会改变 Sentinel Cookie，并最终影响 `email-otp/validate` 和 `create_account` 的动态请求头。

协议状态机将 Sentinel 定义为独立的 HTTP provider：

1. 核心状态机生成当前会话的 endpoint、flow、request ID、device ID、document navigation ID 和 Cookie 快照。
2. `-p` 模式的 HTTP provider 已经用 HTTP 完成 `sentinel/req` 的格式层交换：从 bootstrap、登录页和邮箱验证页合并发现脚本资源，按每个 flow 独立生成 `gAAAAAC...~S` 需求 payload，发送 `p/id/flow`、接收响应 Cookie、解析 token 和 PoW 参数，并按当前 Sentinel SDK 的 FNV 派生逻辑计算 `gAAAAAB...~S` proof。候选 payload 已包含 25 项环境数组、时间源、设备 ID、脚本资源和随机探针；同时会预取 Sentinel SDK 入口、版本脚本和 `frame.html`，并将这些资源的新 Cookie 传回会话。由于 HTTP 没有真实 DOM/window，仍不能证明所有运行时字段与浏览器 SDK 完全等价。协议主链路按每个受保护业务请求准备一次有效 Sentinel proof，不盲目重放 HAR 中的 SDK 预取/刷新请求；`authorize_continue` 仍只作为另一次复登录现场的待验证前置 flow。需要动态 runtime proof 时由同一账号的 Rod 会话运行真实 SDK。
3. 当前真实响应还可能要求 `turnstile.dx` 和 `so.collector_dx/snapshot_dx`。这两类值由 Sentinel SDK 的浏览器运行时 VM 生成，不能从普通 HTTP 响应推导；HTTP provider 必须返回 `ErrSentinelUnsupported`，不能伪造空值、使用 HAR 历史值或把一次响应跨流程复用。
4. Cloudflare 返回 Challenge、403 或 429 时返回 `ErrProtocolChallenge`。
5. 不调用外部代解服务，不伪造或重放 Challenge/Sentinel 值。

因此，协议实现已经覆盖可独立验证的协议层，但尚未达到“完整注册成功”的 100% 纯协议验收：动态 Turnstile/Session Observer 证明仍是明确边界。正式的 `-p` 模式在该边界使用同一账号的 Rod 会话生成 proof；默认模式则由完整浏览器流程处理所有步骤。

补充：`../scenemint-pro` 中的 SHA3-512 PoW 实现对应 ChatGPT 图像接口的 `chat-requirements`，不是本注册流程的 `/sentinel/req`。本流程沿用线上 Sentinel SDK 对 `gAAAAAC/gAAAAAB` 的 FNV 派生格式，注册端点的脚本 fallback 也使用 `sentinel.openai.com/sentinel/<version>/sdk.js`，不能把两者混用。

## 6. HTTP 客户端实现

建议使用 Go 标准库：

- `net/http`
- `net/http/cookiejar`
- `encoding/json`
- `net/url`
- `context.Context`

客户端要求：

- 禁止共享跨账号 CookieJar。
- 禁止直接复制 HAR 的 `Cookie` 请求头。
- 手动控制 302，以便记录 Location、Cookie 和阶段。
- 响应体设置上限，避免错误响应无限占用内存。
- 错误包含阶段、URL、状态码和响应大小，不包含响应正文秘密值。
- `create_account` 和 `email-otp/validate` 失败时保留原始状态码，不静默降级。

当前实时页面/请求的网页版本基线：

```text
User-Agent: Chrome/152 on macOS
OAI-Client-Version: prod-c4ad2074065cc40142f2fa2e09294009480c7d3f
OAI-Client-Build-Number: 10547157
```

这些值集中在协议配置中，后续网页版本变化时只修改一个位置并更新 fixture。

当前代码已经落地确定性协议状态机：

- ProtocolFlow 独立创建 http.Client 和 cookiejar.Jar。
- 默认 cmd/toapi 入口使用完整浏览器认证；仅传入 `-p` 时使用协议混合认证。
- 协议和浏览器都实现同一个 Authenticator 接口，邮箱、账号存储、SceneMint 上传和调度逻辑共用。
- `-p` 提供受限混合模式：仅在 HTTP Sentinel 响应明确要求 runtime proof，或 HTTP 请求收到明确 Cloudflare Challenge 时惰性启动 Rod；Rod 只生成 proof、恢复挑战并同步 Cookie，其余认证请求仍由协议完成。
- 每个账号独立创建临时 profile、Rod 进程和浏览器 context；同一账号内复用该会话，账号结束后统一关闭。

### 6.2 受限混合模式：Rod 只生成 runtime proof

`-p` 的边界固定为：

```text
协议状态机：bootstrap、OAuth、CSRF、authorize、OTP 收取、OTP/资料请求构造、callback、session
Rod：在当前账号的 Background page 中加载 email/about-you 页面，运行真实 SentinelSDK.token(flow) 和 sessionObserverToken(flow)，导出 proof 与 Cookie；页面关闭但账号 context 保持到本次认证结束
```

流程如下：

1. HTTP provider 先按正常协议请求 `/sentinel/req`。
2. 只有响应明确包含 `turnstile.required` 或 `so.required` 时，才进入 Rod provider。
3. Rod 使用当前账号独立进程和临时 Profile 的浏览器 context，导入当前协议会话 Cookie 和 HTTP Sentinel 预取产生的 Cookie；页面使用 Background Target，脚本运行当前版本 Sentinel SDK，生成一次性 Turnstile/SO proof。
4. Rod 只返回 proof 与浏览器 Cookie，随后关闭当前页面；账号 context 继续保留，协议客户端把 Cookie 写回当前 CookieJar，并使用 Go HTTP 发送当前的 `email-otp/validate` 或 `create_account`。
5. Rod 不填写 OTP、不点击 validate、不填写姓名/生日、不发送业务 POST、不跟随 callback；请求体和业务参数始终由协议状态机生成。
6. 如果未来服务端证明业务 POST 也必须与浏览器网络上下文绑定，再基于真实失败证据扩大 Rod 边界；当前不默认引入浏览器 fetch。

对于 `-p` 模式，如果任意协议请求返回明确的 Cloudflare Challenge，Rod 在当前账号的 Background page 中打开该 URL 一次等待挑战完成、导出 Cookie，协议客户端随后只重试原请求一次。

`-p` 不是历史 HAR Token 重放，也不是完整浏览器回退。它使用 headful Chrome 的 Background page；如果 Turnstile 变为需要人工交互，后台页面可能无法完成，流程会在 runtime proof 阶段报告失败，不调用外部代解服务。默认不加 `-p` 时使用完整浏览器流程。

### 6.3 账号级浏览器会话

`-c` 仍然按账号串行执行，但认证器不再跨账号复用。每次账号尝试创建一套临时浏览器 profile、Chrome 进程和 Rod 连接；默认浏览器模式和 `-p` 混合模式都在该账号浏览器 context 内完成，账号结束后关闭 Chrome 并清理临时 profile。

默认浏览器模式使用一个 Background page 完成完整认证。`-p` 模式默认不启动 Rod；只有命中 runtime proof 或 Challenge 边界时才为当前账号创建 Rod context，之后复用该会话生成所有需要的证明或 Challenge Cookie，页面可以按阶段关闭，但账号会话保持到当前认证结束；业务请求仍由该账号独立的 HTTP Client 和 CookieJar 发送。

正式批处理不使用固定的 `~/.config/rod/data`，也不在 Chrome 运行时删除 profile。每个账号创建新的临时 profile 只能隔离 Cookie、LocalStorage、IndexedDB 和 profile 状态，不代表完整硬件或网络指纹随机化；User-Agent、平台、语言、视口和协议环境应保持一致。

### 6.4 参考 `chatgpt2api` 历史实现后的结论

该仓库当前主分支已经移除注册功能；注册相关代码应查看历史 `v1.6.0`，不能只看当前主分支。可复用的核心经验有：

- 每个账号使用独立会话，显式维护 `oai-did`、Cookie、PKCE 和重定向；HTTP 层使用 Chrome impersonation，而不是简单复制浏览器请求头。
- `utils/sentinel.py` 提供旧版本 `/sentinel/req` 的 FNV/PoW 实现；`utils/pow.py` 的 SHA3 PoW 是另一条 Chat Requirements 链路，不能与注册 Sentinel 直接混用。
- `utils/turnstile.py` 能按 `turnstile.dx` 解码一类 Turnstile token，但没有覆盖当前 HAR 中的 Session Observer collector/snapshot runtime proof，因此不能替代 Rod runtime。
- 历史注册链路是 `api/accounts/user/register`、`email-otp/send`、`create_account`、`continue_url` 和 Platform OAuth token 交换；当前 HAR 是 ChatGPT NextAuth callback/session 链路，不能直接移植旧 endpoint 或旧 flow 名称。
- 该仓库针对 Cloudflare 的修复说明了：不能仅凭 `Server: cloudflare` 或任意 429 判断 Challenge；应结合状态码、响应头和正文标记。当前协议层已采用同样的分类原则。

因此，参考项目适合用于校准 HTTP 会话和 Sentinel 辅助算法；当前注册主链路仍以实时 HAR 和当前响应为准。若纯 HTTP 在 bootstrap 持续收到 403，下一步应检查 Chrome TLS/HTTP 指纹或合规的代理清障能力，而不是继续堆叠旧 HAR 字段。

### 6.5 当前真实验证结果

已用 SunMail 测试邮箱和可见浏览器执行完整注册，并用 SunMail 实际运行过协议入口，结果如下：

```text
邮箱地址获取       成功
ChatGPT bootstrap   成功
OAuth signin/302    成功
email-verification  成功
SunMail 收取 OTP    成功
浏览器 Sentinel req 成功，返回 token/PoW/Turnstile/SO 要求
浏览器 OTP validate 成功
浏览器 create_account 成功
浏览器 callback     成功
浏览器 ChatGPT 会话 成功
协议混合入口        注册成功，HTTP 业务流程和 Rod 边界可用
```

实时浏览器流程已再次创建测试账号并进入 ChatGPT 首页，证明普通 HTTP 字段、Cookie 迁移和 callback 顺序已核实；本次保留日志还确认邮箱验证码错误会停留在验证页，正确验证码随后进入 `/about-you`，提交资料后进入 ChatGPT 首页。协议入口也已真实走到收取 OTP 后的 `sentinel/req`。内部纯 HTTP provider 仍可能在 Sentinel 动态 Turnstile/Session Observer proof 阶段停止，但正式 CLI 通过 `-p` 使用混合 provider。

随后用同一临时账号执行退出后复登录，实时 Network 记录确认了活跃账号的成功路径：`screen_hint=login_or_signup` -> OAuth/邮箱验证码 -> `email-otp/validate` -> ChatGPT callback -> `/api/auth/session/`，没有 `create_account`。已有账号的协议状态机已经按这条路径实现；它与注册共用普通 HTTP 链路，但仍受相同的动态 Sentinel proof 阻塞。

此前旧版混合模式曾把浏览器 proof 与 HTTP 业务请求组合在不完整的状态边界中，出现 `about-you` 跳转；补充 OTP 后的 about-you 状态迁移并正确同步 Cookie scope 后，进一步验证了“Rod 只生成 proof、业务请求回到 Go HTTP”的边界。token-only 版本在切换后连续 5/5 成功；加入 Challenge 条件恢复后又连续 8/8 成功，其中 1 次真实触发首页 403 Challenge 并由 Rod 清障后继续完成，未再出现业务流程失败。此前修复前的 OTP 重发 400 和实验期间的 bootstrap 403 已分别通过重发容错与条件恢复处理。

## 7. 错误分类

```text
ErrProtocolBootstrap       初始化页面失败
ErrProtocolCSRF            CSRF 缺失或无效
ErrProtocolOAuth           OAuth URL、state 或 callback 异常
ErrProtocolChallenge       Cloudflare/上游 Challenge
ErrSentinelUnsupported     无法生成当前认证所需 Sentinel 数据
ErrSentinelRuntimeProof    Sentinel 响应要求浏览器运行时证明
ErrProtocolOTP             OTP 请求或校验失败
ErrProtocolAccount         create_account 失败
ErrProtocolSession         callback 后未获得 accessToken
ErrAccountDeactivated      账号停用
ErrMailCodeTimeout         邮件验证码超时
```

错误必须带阶段信息，便于后续用浏览器对照同一阶段。

## 8. 测试策略

### 单元测试

- URL/query/form/json 构造。
- CookieJar 跨域、Path、Secure 和分片 Cookie。
- 302 Location 解析和 host 校验。
- CSRF、signin URL、session accessToken 响应解析。
- 403/429/`CF-Mitigated: challenge` 错误映射。
- Sentinel 需求 payload、PoW proof、请求头/Cookie、响应解析和动态运行时 proof 边界。
- OTP 超时、取消和最多 5 次重发。

### 集成测试

使用脱敏 HAR 或本地 RoundTripper fixture，验证完整顺序：

```text
bootstrap -> csrf -> signin -> authorize -> otp -> about-you -> create -> callback -> session
```

已有账号分支验证顺序为：

```text
bootstrap -> csrf -> signin -> authorize -> otp -> callback -> session
```

测试 fixture 只保留字段名、状态码、Cookie 属性和响应结构，不保留真实 Token、OTP、Cookie value、OAuth code 或个人资料。

### 实际验证

使用新的测试邮箱运行真实协议流程。失败时记录：

- 最后成功阶段；
- 方法、host、path、状态码；
- 请求/响应字段名；
- Cookie 名称和属性；
- 是否出现 Challenge；
- 响应正文的脱敏结构。

默认浏览器模式和 `-p` 协议混合模式由 CLI 明确选择；不把失败隐藏为成功，也不因普通 HTTP 错误隐式切换完整浏览器流程。

## 9. 完成标准

协议混合实现的验收标准：

1. 默认入口使用完整浏览器；显式传入 `-p` 才使用协议混合。
2. `-p` 模式的确定性步骤由 HTTP 客户端完成，Rod 只处理明确的 runtime proof/Challenge。
3. 每个账号使用独立临时 profile、Rod 会话、浏览器 context 和 HTTP CookieJar。
4. 能够从 callback/session 获得非空 accessToken。
5. Sentinel/Challenge 无法完成时返回可定位的明确错误。
6. 通过脱敏 fixture 单元测试和协议顺序测试。
7. 新账号注册和已有账号 Renew 分为两个可独立验证的流程；两条主链路均可选择完整浏览器或协议混合模式。

当前状态：第 1、2、3、5、6、7 项已具备实现或 fixture/实时记录验证；第 4 项仍受动态 Turnstile/Session Observer 证明阻塞，因此不能宣称真实生产流程已经达到 100% 纯协议成功。

## 10. 继续完成 100% 协议实现的前置条件

当前阻塞不是缺少普通请求字段：`sentinel/req` 已能通过 HTTP 发起并解析响应，但上游返回的 `turnstile.dx`、`so.collector_dx` 和 `so.snapshot_dx` 是绑定运行时环境的动态程序。单纯补充旧 HAR、复制历史请求头或重放一次浏览器 token 都不能形成可重复的协议实现。

要继续完成纯协议闭环，需要满足以下条件之一：

1. 提供允许测试账号跳过动态 Sentinel 证明的上游测试/白名单环境；或
2. 提供上游认可的、非浏览器运行时的正式证明接口。

如果只能由浏览器运行该证明，则 `-p` 只能称为协议混合实现，不能称为 100% 纯协议。已有账号 Renew 同样按账号创建独立浏览器会话，并复用协议状态机和混合 Sentinel provider。
