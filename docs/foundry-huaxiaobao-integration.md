# Foundry—Huaxiaobao 接入说明

## 基线与边界

- 上游：`xpzouying/xiaohongshu-mcp`
- 基线提交：`aad2a3d249a347859975ce3b76d3442c4a027780`
- 改造分支：`codex/foundry-huaxiaobao-integration`
- 许可证：Apache-2.0。升级和分发时必须保留许可证、版权及修改声明。

Huaxiaobao 是本工具唯一的自动化执行所有者，负责 MCP/HTTP 连接、账号状态、登录现场、权限审核、执行和工具回执。Foundry 只调用版本化能力合同，不复制 Cookie、Token、二维码或浏览器会话。

Foundry 拥有销售目标、内容计划、客户/商机语义、预算、报价、外部动作批准、独立验收与会计裁决。工具成功、帖子存在或互动发生都不能直接证明客户验收、收入或净值。

## 能力与副作用

| 能力 | 当前入口 | 副作用分类 | 接入要求 |
| --- | --- | --- | --- |
| 登录状态 | `check_login_status` | 只读 | 回执需包含账号不透明引用和已核验用户 ID |
| 登录二维码 | `get_login_qrcode` | 创建临时登录会话；MCP 不标只读 | 仅账号所有者可进入受保护现场；二维码不得进入普通日志或 ledger |
| Feeds、搜索、详情、主页 | 对应 MCP/HTTP 读取接口 | 通常只读 | 页面导航可能改变站点侧浏览状态，按能力逐项审核 |
| 未读数 | `get_unread_count` | 只读 | 不清除未读标记 |
| 通知列表 | `list_notifications` | **会清除所选分区未读标记** | 不得声明为只读；执行前按有副作用读取授权 |
| 发布、评论、回复、点赞、收藏 | 对应 MCP/HTTP 写接口 | 外部动作 | 必须携带仍有效的具名批准引用；工作人员不能自批 |
| 删除 Cookie | `delete_cookies` | 破坏性账号操作 | 仅 Huaxiaobao/工具管理员执行，并创建重新登录依赖 |

`xiaohongshu-mcp` 是小红书主执行器。若同时部署 `social-auto-upload`，其小红书发布能力只能作为显式备用；同一账号/动作不得并行双执行。

## 安全 adapter surface 与外发门禁

`go run ./cmd/huaxiaobao-adapter describe` 输出机器可读能力描述；`go run ./cmd/huaxiaobao-adapter execute` 从标准输入读取 `foundry.huaxiaobao.tool-request.v1` JSON。当前只开放 `account.status`，它调用原生 `/api/v1/login/status` 并返回稳定账号对象引用、请求哈希和 `READY`/`BLOCKED`/`UNKNOWN` typed result。`XHS_ADAPTER_BASE_URL` 默认是本机 `http://127.0.0.1:18060`；`XHS_ADAPTER_AUTH_TOKEN` 必须通过 Huaxiaobao 进程环境注入，adapter 不接受请求内凭据，也不输出令牌。

原生发布、评论、回复、点赞和收藏服务新增 `XHS_ENABLE_EXTERNAL_ACTIONS` fail-closed 门禁。默认、空值和未知值全部拒绝，并且在启动浏览器或访问账号前返回；只有 `1`、`true`、`yes`、`on` 显式开启。该兼容开关不等于 Foundry 具名批准，当前安全 adapter 仍完全不暴露外发能力。

## 账号与人工入口

缺少账号、Cookie 失效或需要扫码时，Huaxiaobao 应返回持久化阻塞结果，由 Foundry 建立或关联人工依赖：

- 身份：账号所有者，而不是普通外包工作人员；
- 入口：Huaxiaobao 管理的临时、受限登录现场；
- 完成条件：适配器重新调用登录检查并核对预期账号/租户；
- 失败/取消/过期：保持原自动化检查点暂停，不持续重试发布；
- 恢复：工具验证成功后产生绑定原依赖的 typed outcome，原组件 ACK 消费后才恢复。

普通工作人员可处理获准的内容整理或客服工作，但不能获得账号所有权、管理员权限或批准权。任何密码、Cookie、Token、二维码和可复用会话链接都不得写入任务正文、普通日志或 Foundry ledger。

## 部署与升级

生产部署由 VolvenceDeploy 接收获批的部署规格后执行。持久化数据目录保存 Cookie，但必须位于 Huaxiaobao 执行边界；服务应启用 `AUTH_TOKEN`，限制监听地址和网络访问。不得因本说明擅自重启或替换现有 Foundry/Huaxiaobao 实例。

升级时记录 upstream commit、Fork 改造 commit、Go/依赖版本和镜像 digest；先在隔离环境运行单元测试、MCP 合同测试和无真实外部动作检查，再由发布门批准。

## 当前验收状态

- 源码基线：已记录。
- 本地运行：已用 Go 1.27.0 完成编译和定向测试；尚未启动浏览器服务或连接真实账号。
- 合同：原生 MCP/HTTP 合同存在；已增加 Huaxiaobao 可调用的 `account.status` 最小版本化 adapter，外发合同仍未开放。
- 真实账号：未提供，未验证。
- 获批动作：未执行。
- 人工 ACK、服务重启、重复/迟到结果、权限撤销和检查点恢复：尚无真实证据。

本次 `go test . ./cmd/huaxiaobao-adapter` 通过。`go test ./...` 仅在既有
`cookies/TestGetCookiesFilePath/本地没有时兜底到tmp旧路径` 上失败：Windows
短路径临时目录期望与工作目录中的 `cookies.json` 选择不同；新增根包和 adapter
测试均通过。该环境差异不代表全量测试通过，也未被改写为新功能证据。

不得用 fixture、页面跳转或本机健康检查声称真实获客、客户验收或收入。
