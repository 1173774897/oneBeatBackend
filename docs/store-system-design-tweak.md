# 收藏小铺 IAP 订单与对账方案调整

## 1. 文档目的

本文是 `docs/store-system-design.md` 中 IAP 订单、订阅、退款和对账设计的调整方案。
原文中商品目录、口令兑换、角色限免和通用权益模型仍然有效；涉及 Huawei IAP
订单生命周期的部分，以本文为准。

项目尚未上线，测试数据不需要保留。因此本次实施采用直接重建 IAP 表和一次性切换业务逻辑的方式，
不引入旧表回填、双写、兼容读取、历史错误状态修复等过渡逻辑。

## 2. 调整目标

本次调整需要解决以下问题：

1. 首次购买、每期续费和退款是不同的财务事实，不能都压缩到一条可变订单记录中。
2. `purchaseToken` 用于关联订阅关系、购买周期和退款，但不能作为交易表唯一主键。
3. `purchaseToken` 或 `purchaseOrderId` 变化只表示订阅可能进入新一期，不构成撤销证据。
4. 取消自动续费但本期尚未到期的订阅仍然有效。
5. 月卡权益只覆盖已确认付费周期；周期到期即失效，扣费重试过程不建立本地业务状态。
6. 退款财务事实与权益效果解耦，不能仅凭 `tradeType=REFUND` 回收权益。
7. 沙盒订单和 0 元订单不能依赖支付订单查询补偿。
8. 生产对账必须满足时间窗口、历史范围和 `continuationToken` 分页约束。
9. 重复通知、重复恢复购买、分页重试和任务重跑都必须保持幂等。

## 3. 权威数据来源与职责

| 数据来源 | 职责 | 是否形成完整历史 |
| --- | --- | --- |
| Huawei 关键事件通知 | 实时接收购买、续费、撤销和退款事件 | 主渠道，但可能延迟或重复 |
| `QuerySubscription` | 验证当前订阅状态、到期时间和最新一期 | 否，只表示当前权威快照 |
| `trade/orders/query` | 次日补查生产环境的 `PURCHASE`、`REFUND` 财务流水 | 仅覆盖最近 180 天，作为生产补偿与对账渠道 |
| 客户端恢复购买 | 提供当前账号可见的购买凭据并触发用户级复核 | 否，不能作为完整交易历史 |

两个状态查询接口的定位和入参必须严格区分：

| 接口 | 正确入参 | 用途 |
| --- | --- | --- |
| `QuerySubscription` | `purchaseOrderId + purchaseToken` | 查询自动续费订阅当前状态（Harmony 服务端 API 必填二者） |
| `QueryOrder` | `purchaseOrderId + purchaseToken` | 查询消耗型、非消耗型或非续期订阅的单笔订单状态 |

通知与客户端 JWS 里的 `purchaseOrderId` 用于调用 `QuerySubscription` / `QueryOrder` 与交易幂等。
通知里的 `subscriptionId` 对应商品 `huawei_product_id`，**不能**当作 `purchaseOrderId` 传入状态查询。
不得拿 `QuerySubscription` 返回的 `lastPurchaseOrder.purchaseOrderId` 与 `QueryOrder(退款单号)` 的结果做相等性对账。

服务端处理顺序为：

```text
关键事件通知
    -> 保存并验签通知
    -> QuerySubscription 验证当前状态
    -> 写交易、周期和订阅快照
    -> 重新计算权益

生产次日对账
    -> trade/orders/query 分页查询
    -> 幂等补齐遗漏交易
    -> 对受影响订阅执行 QuerySubscription
    -> 重新计算权益
```

沙盒环境只验证“事件通知 + `QuerySubscription`”链路。`trade/orders/query` 的客户端实现、
分页和重试通过固定响应或 fake Huawei client 测试，不能以沙盒查询结果作为验收依据。

## 4. 状态与权益规则

### 4.1 订阅状态标准化

数据库内部使用以下状态：

| 内部状态 | 含义 | 当前月卡权益 |
| --- | --- | --- |
| `ACTIVE` | 当前周期有效且自动续费开启 | 有效 |
| `CANCELED_ACTIVE` | 已取消自动续费，但当前已付费周期尚未结束 | 有效 |
| `EXPIRED` | 当前周期已到期 | 无效 |
| `REVOKED` | 已撤销订阅 | 无效 |

Huawei 返回“当前有效但自动续费关闭”时，必须标准化为 `CANCELED_ACTIVE`，不能标准化为
`EXPIRED`。`auto_renewing` 只描述下一期是否续费，不单独决定本期权益。

Huawei `lastSubscriptionStatus` 的映射固定如下：

| Huawei `status` | `renewalInfo.autoRenewStatusCode` | `hasInBillingRetryPeriod` | OneBeat 状态 | 权益 |
| --- | --- | --- | --- | --- |
| `1`（生效中） | `1`（开启） | 任意 | `ACTIVE` | 仅 `expires_at > now()` 时有效 |
| `1`（生效中） | `0`（关闭） | 任意 | `CANCELED_ACTIVE` | 仅 `expires_at > now()` 时有效 |
| `2`（已到期） | 任意 | 任意 | `EXPIRED` | 无效 |
| `3`（尝试扣费） | 任意 | 通常为 `true` | `EXPIRED` | 无效 |
| `5`（已撤销） | 任意 | 任意 | `REVOKED` | 无效 |

`CANCELED_ACTIVE` 不是 Huawei `status` 的独立值，必须由 `status=1` 与
`autoRenewStatusCode=0` 组合得出。`hasInBillingRetryPeriod` 保留在 Huawei 原始脱敏快照中用于审计，
不建立本地权益分支。

当前畅游月卡的有效判断固定为：

```text
revoked_at IS NULL
AND status IN ('ACTIVE', 'CANCELED_ACTIVE')
AND expires_at > now()
```

`expires_at` 始终表示已经付费周期的截止时间。Huawei 不为进入“尝试扣费”状态发送关键事件通知，
OneBeat 也不为发现该中间状态增加定时轮询或独立 `GRACE` 状态。付费周期到期后，权益直接因
`expires_at <= now()` 失效；若某次必要的 `QuerySubscription` 恰好返回状态 3，则业务层统一归一为
`EXPIRED`，原始 Provider 响应仍保留在脱敏快照中用于审计。收到 `BILLING_RECOVERY` 后再次执行
`QuerySubscription`，只有权威结果恢复为未到期 `ACTIVE` 才重新授予权益。

这里的“不轮询”只针对主动发现中间状态。购买/恢复购买、关键事件通知复核和生产交易对账仍必须按
各自流程调用 `QuerySubscription`，不能只信客户端或通知名称。

不得仅凭 `latest_purchase_order_id` 存在、订单已发货或自动续费开启来判定权益有效。

### 4.2 Huawei 关键事件通知清单

Huawei webhook 只推送以下预定义关键事件，不承诺覆盖任意订阅状态变化：

| 主类型 | 子类型 | Huawei 语义 | OneBeat 处理 |
| --- | --- | --- | --- |
| `DID_NEW_TRANSACTION` | `INITIAL_BUY` | 首次购买 | 验签后调用 `QuerySubscription`，写首购交易与周期 |
| `DID_NEW_TRANSACTION` | `DID_RENEW` | 续期成功 | 验签后查询权威快照，写新续费交易与周期 |
| `DID_NEW_TRANSACTION` | `RESTORE` | 恢复订阅 | 仅作为同步触发器；查询后幂等更新，不推断退款 |
| `DID_NEW_TRANSACTION` | `BILLING_RECOVERY` | 重试扣费成功 | 查询确认新一期 `ACTIVE` 后恢复权益 |
| `DID_CHANGE_RENEWAL_STATUS` | `AUTO_RENEW_ENABLED` | 开启自动续费 | 查询后更新 `auto_renewing` 与标准化状态，不写财务交易 |
| `DID_CHANGE_RENEWAL_STATUS` | `AUTO_RENEW_DISABLED` | 关闭自动续费 | 未到期时更新为 `CANCELED_ACTIVE`，不写财务交易 |
| `DID_NEW_TRANSACTION` / `DID_CHANGE_RENEWAL_STATUS` | `UPGRADE` / `DOWNGRADE` | 订阅升降级 | 查询权威商品、周期和续费状态后幂等落库 |
| `DID_CHANGE_RENEWAL_STATUS` | `PRICE_INCREASE` | 用户同意涨价 | 更新续费相关快照，不直接改变当前权益 |
| `REVOKE` | `REFUND_TRANSACTION` | 退款成功 | 写独立退款交易，并按权威订阅终态决定权益效果 |
| `REVOKE` | `APPLICATION_DELETE_SUBSCRIPTION_HOSTING` | 撤销订阅 | 写撤销终态并回收权益 |
| `EXPIRE` | `BILLING_RETRY` | 最终进入保留期 | 查询后更新为 `EXPIRED`，不生成退款交易 |
| `EXPIRE` | `VOLUNTARY` | 主动退订后到期 | 查询后更新为 `EXPIRED`，不生成退款交易 |
| `EXPIRE` | `PRODUCT_NOT_FOR_SALE` | 商品已不可售 | 查询后更新为 `EXPIRED`，不生成退款交易 |
| `RENEWAL_TIME_MODIFIED` | `RENEWAL_EXTENDED` | 延迟续订日期 | 查询后更新当前付费周期截止时间 |

扣费失败后进入 Huawei 状态 3 的中间过程没有对应 webhook。OneBeat 不轮询该过程，也不等待它来回收
权益；已付费周期一旦到达 `expires_at`，查询权益时自然失效。所有 webhook 均先验签，再调用对应
Huawei 查询接口复核，不能只按通知名称直接改写权益。

### 4.3 财务状态与权益效果分离

交易记录保存 Huawei 已经发生的财务事实；权益效果保存 OneBeat 对该事实的业务解释。

```text
trade_type:          PURCHASE | REFUND
transaction_subtype: INITIAL | RENEWAL | REFUND
refund_type:         WITHDRAWAL | RETURN_FEE | USER_REFUND | UNKNOWN
entitlement_effect:  NONE | KEEP | REVOKE | PENDING
```

退款决策规则：

| 来源 | 初始 `refund_type` | 初始权益效果 | 后续动作 |
| --- | --- | --- | --- |
| 已验签 `REVOKE` 通知 | `WITHDRAWAL` | `REVOKE` | 写终态并回收权益 |
| 能匹配本系统返费请求 | `RETURN_FEE` | `KEEP` | 保留本期权益并刷新订阅状态 |
| 用户或平台退款通知 | `USER_REFUND` | `PENDING` | 调用 `QuerySubscription` 后决定 |
| 仅由次日对账发现且无法归因 | `UNKNOWN` | `PENDING` | 查询订阅状态并保留告警上下文 |

`USER_REFUND` 查询后的处理：

```text
QuerySubscription 返回 ACTIVE/CANCELED_ACTIVE 且 expires_at 未到期
    -> entitlement_effect = KEEP

QuerySubscription 返回状态 3、REVOKED 或 EXPIRED 等无权益状态
    -> entitlement_effect = REVOKE

查询失败或结果冲突
    -> entitlement_effect = PENDING
    -> 不直接回收权益，进入重试和告警
```

已知 `RETURN_FEE` 原则上保留权益。如果其订阅查询结果与预期冲突，不静默覆盖为
`REVOKE`，而是保持 `PENDING` 并记录告警，等待重试或人工检查。

## 5. 目标数据库模型

项目尚未上线，本次直接替换现有 `iap_orders` 和 `iap_subscriptions` 结构，不保留旧测试数据。
`entitlement_grants`、`iap_webhook_events` 和 `entitlement_audit_logs` 可保留表名，但同步调整字段和约束。

### 5.1 `iap_subscriptions`

一行表示一个稳定的逻辑订阅关系，不表示某一期订单。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `provider` | `text` | 当前为 `HUAWEI` |
| `environment` | `text` | `SANDBOX`、`PRODUCTION` |
| `user_id` | `uuid` | 购买用户 |
| `item_key` | `text` | OneBeat 权益键 |
| `huawei_product_id` | `text` | Huawei 商品 ID |
| `subscription_key` | `text` | 服务端确定的稳定订阅关系标识 |
| `latest_period_id` | `uuid` | 最新订阅周期，可为空 |
| `latest_purchase_order_id` | `text` | 最新查询到的购买订单号 |
| `status` | `text` | 标准化订阅状态 |
| `auto_renewing` | `boolean` | 是否继续自动续费 |
| `starts_at` | `timestamptz` | 订阅关系开始时间 |
| `expires_at` | `timestamptz` | 当前已确认付费周期到期时间，不包含宽限期 |
| `revoked_at` | `timestamptz` | 已撤销时间，可为空 |
| `verified_at` | `timestamptz` | 最近权威查询时间 |
| `version` | `bigint` | 乐观更新或快照顺序控制 |
| `created_at` / `updated_at` | `timestamptz` | 审计时间 |

约束和索引：

```text
UNIQUE(provider, environment, user_id, huawei_product_id, subscription_key)
INDEX(user_id, status, expires_at)
CHECK(expires_at > starts_at)
```

`subscription_key` 的 Provider 来源属于实现前待确认项：必须先用 Huawei 当前官方字段定义和
沙盒真实响应确认是否存在跨续期稳定的订阅链标识，不能默认存在类似其他平台的稳定订阅订单号。
如果 Huawei 没有提供稳定链标识，仅允许把新 token 绑定到该用户、该商品唯一且尚未终止的
订阅关系；存在歧义时创建新关系并告警，不能只用最新 token 直接生成新的订阅关系。

### 5.2 `iap_subscription_tokens`

记录订阅生命周期中出现过的所有 token。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `provider` | `text` | 当前为 `HUAWEI` |
| `environment` | `text` | `SANDBOX`、`PRODUCTION` |
| `subscription_id` | `uuid` | 逻辑订阅关系 |
| `purchase_token_hash` | `bytea` | HMAC/SHA-256 查询键 |
| `purchase_token_ciphertext` | `bytea` | 加密 token，供服务端查询 |
| `first_seen_at` | `timestamptz` | 首次发现时间 |
| `last_seen_at` | `timestamptz` | 最近发现时间 |
| `source` | `text` | `WEBHOOK`、`CLIENT_RESTORE`、`RECONCILIATION` |

约束：

```text
UNIQUE(provider, environment, purchase_token_hash)
INDEX(subscription_id, last_seen_at DESC)
```

token 是订阅关联字段。它在交易表中必须建索引，但购买和退款可以共享 token，因此不能在交易表中唯一。

### 5.3 `iap_subscription_periods`

一行表示一个已确认的付费周期。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `provider` | `text` | 当前为 `HUAWEI` |
| `environment` | `text` | `SANDBOX`、`PRODUCTION` |
| `subscription_id` | `uuid` | 逻辑订阅关系 |
| `purchase_transaction_id` | `uuid` | 对应购买/续费交易 |
| `purchase_token_id` | `uuid` | 该期使用的 token |
| `purchase_order_id` | `text` | 该期购买订单号 |
| `starts_at` | `timestamptz` | 周期开始 |
| `expires_at` | `timestamptz` | 周期结束 |
| `status` | `text` | `PAID`、`EXPIRED`、`REVOKED` |
| `created_at` / `updated_at` | `timestamptz` | 审计时间 |

约束：

```text
UNIQUE(provider, environment, purchase_order_id)
UNIQUE(purchase_transaction_id)
CHECK(expires_at > starts_at)
```

### 5.4 `iap_provider_transactions`

保存不可丢失的 Provider 财务流水。首次购买、每期续费和每笔退款分别占一行。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `provider` | `text` | 当前为 `HUAWEI` |
| `environment` | `text` | `SANDBOX`、`PRODUCTION` |
| `user_id` | `uuid` | OneBeat 用户 |
| `subscription_id` | `uuid` | 订阅关系，可为空 |
| `item_key` | `text` | OneBeat 权益键 |
| `huawei_product_id` | `text` | Huawei 商品 ID |
| `purchase_order_id` | `text` | 当前财务订单号 |
| `purchase_token_hash` | `bytea` | token 查询索引 |
| `purchase_token_ciphertext` | `bytea` | 加密 token |
| `trade_type` | `text` | `PURCHASE`、`REFUND` |
| `transaction_subtype` | `text` | `INITIAL`、`RENEWAL`、`REFUND` |
| `refund_type` | `text` | 非退款为空，其余使用退款枚举 |
| `entitlement_effect` | `text` | `NONE`、`KEEP`、`REVOKE`、`PENDING` |
| `related_purchase_transaction_id` | `uuid` | 退款指向被退款购买期 |
| `provider_status` | `text` | Provider 财务状态 |
| `amount` / `currency` | `numeric` / `text` | 金额和币种 |
| `occurred_at` | `timestamptz` | Provider 交易时间 |
| `acknowledged_at` | `timestamptz` | 发货确认时间 |
| `verified_at` | `timestamptz` | 最近验证时间 |
| `payload_snapshot` | `jsonb` | 已遮蔽敏感 token 的原始快照 |
| `created_at` / `updated_at` | `timestamptz` | 审计时间 |

关键约束：

```text
UNIQUE(provider, environment, purchase_order_id, trade_type)
INDEX(provider, environment, purchase_token_hash)
INDEX(subscription_id, occurred_at DESC)
INDEX(related_purchase_transaction_id)

trade_type = 'PURCHASE' -> refund_type IS NULL
trade_type = 'REFUND'   -> refund_type IS NOT NULL
trade_type = 'REFUND'   -> entitlement_effect IN ('KEEP', 'REVOKE', 'PENDING')
```

退款关联顺序：

1. 使用退款通知或订单中的 `purchaseToken` 查询 `iap_subscription_tokens`。
2. 在同一订阅下查找 token 对应的购买周期。
3. 将退款交易的 `related_purchase_transaction_id` 指向购买/续费交易。
4. 如果无法唯一定位，保留空关联、设置 `PENDING` 并告警，不能猜测某一期。

### 5.5 `iap_refund_requests`

记录 OneBeat 服务端主动发起的返费操作，用于区分 `RETURN_FEE` 与用户自行退款。

至少保存：请求 ID、订阅 ID、目标购买交易 ID、purchase token hash、操作类型、请求状态、
请求时间、完成时间和遮蔽后的响应快照。

### 5.6 `iap_webhook_events`

保留现有表名并扩展：

```text
provider
environment
huawei_event_id
notification_type
notification_subtype
signature_valid
payload
status
attempt_count
last_error
received_at
processed_at
```

幂等约束调整为：

```text
UNIQUE(provider, environment, huawei_event_id)
```

通知必须先落库，再处理业务。重复或乱序通知只允许更新处理状态，不得重复发放或回收权益。

### 5.7 `iap_reconciliation_checkpoints`

保存生产对账任务的精确进度：

```text
provider
environment
job_name
direction
window_start
window_end
continuation_token
page_number
status
last_success_at
last_error
updated_at
```

唯一约束：

```text
UNIQUE(provider, environment, job_name)
```

checkpoint 必须区分“时间窗口已经完成”和“窗口内处理到某一分页 token”，避免任务失败后错误跳过未处理页面。
日常对账与首次回补必须使用不同的 `job_name` 和独立 checkpoint，禁止共享同一行进度。

## 6. 业务流程

### 6.1 首次购买和续费通知

1. 先按事件 ID 插入 `iap_webhook_events`；重复事件直接返回成功。
2. 验签失败时保存失败状态，不修改业务表。
3. 使用通知中的 `purchaseOrderId` 与 `purchaseToken` 调用 `QuerySubscription`。
4. 验证应用、商品、账号绑定和环境。
5. upsert `iap_subscriptions`，并登记新的 `iap_subscription_tokens`。
6. 以 `(provider, environment, purchaseOrderId, PURCHASE)` upsert 购买交易。
7. 第一笔购买标记 `INITIAL`，同一订阅后续购买标记 `RENEWAL`。
8. upsert 对应 `iap_subscription_periods`。
9. 更新订阅最新周期和当前状态。
10. 确认发货接口固定：自动续期订阅使用 `confirmSubscriptionPurchase`，消耗型、非消耗型和非续期订阅使用 `confirmPurchase`。
11. 是否执行确认发货，以 Huawei 当前 `finishStatus` 和官方规则为准；确认结果幂等写入 `acknowledged_at`。
12. 按标准化状态和 `expires_at` 重算 `pass.all` 权益。
13. 将通知标记为 `PROCESSED`。

接口边界固定为：消耗型、非消耗型和非续期订阅使用 `confirmPurchase`；自动续期订阅使用
`confirmSubscriptionPurchase`。后端可以分别封装为 `ConfirmOrder` 与 `ConfirmSubscription`，
但方法名不能掩盖两者对应不同 Huawei API。

收到 `BILLING_RECOVERY` 后仍需调用 `QuerySubscription` 验证权威状态；只有结果已经恢复为
`ACTIVE`，才重新授予月卡权益，不能仅凭通知名称直接恢复。

收到 `EXPIRE` 主类型通知（包括 `BILLING_RETRY`、`VOLUNTARY`、`PRODUCT_NOT_FOR_SALE`）时，
调用 `QuerySubscription` 验证后，将订阅和当前周期更新为 `EXPIRED`。该状态变化不生成退款流水，
也不能把原购买交易改成 `REFUNDED`。

收到 `DID_CHANGE_RENEWAL_STATUS` 主类型通知（`AUTO_RENEW_DISABLED` / `AUTO_RENEW_ENABLED`）时，
调用 `QuerySubscription` 验证后更新 `auto_renewing` 与 `status`：未到期的取消续费标准化为
`CANCELED_ACTIVE`，重新开启则恢复 `ACTIVE`。该过程不生成任何交易流水。

### 6.2 恢复购买

恢复购买只是一种用户级同步触发器，不提供撤销证据。

统一规则：

> 恢复购买时 `purchaseToken` 或 `purchaseOrderId` 发生变化，仅表示订阅可能推进到新一期，
> 不构成退款或撤销证据。

恢复流程：

1. 验证客户端提交的当前凭据与登录用户绑定关系。
2. 使用客户端 JWS / 库内 `latest_purchase_order_id` 与当前 `purchaseToken` 调用 `QuerySubscription` 获取权威快照。
3. 新 token 写入 `iap_subscription_tokens`，不覆盖或删除旧 token。
4. 新订单写为新的 PURCHASE 交易和订阅周期，不修改旧交易为 `REVOKED`。
5. 相同凭据重复恢复只执行 upsert 和刷新 `verified_at`。
6. 只有已验签 `REVOKE`、退款通知或退款订单才能把交易记为退款/撤销；`QuerySubscription`
   返回状态 3 或自然 `EXPIRED` 可以停止当前访问，但不能据此生成退款流水或把购买交易改成 `REVOKED`。

必须删除当前实现中“最新订单号不同就把旧订单标记为 `REVOKED`”的逻辑。
自然到期只能更新订阅/周期为 `EXPIRED`，不能把购买交易改成 `REFUNDED`。

### 6.3 退款处理

1. 按事件 ID 保存并验签通知。
2. 幂等写入 `trade_type=REFUND` 的交易。
3. 使用 token 关联逻辑订阅和原购买期。
4. 根据通知类型和 `iap_refund_requests` 确定 `refund_type`。
5. `WITHDRAWAL` 可直接标记 `REVOKE`，同时刷新订阅终态。
6. `RETURN_FEE` 默认 `KEEP`，但仍刷新订阅状态以发现异常。
7. `USER_REFUND` 和 `UNKNOWN` 初始为 `PENDING`。
8. 对 `PENDING` 调用 `QuerySubscription`；返回状态 3、`REVOKED` 或 `EXPIRED` 时转为 `REVOKE`，返回未到期 `ACTIVE/CANCELED_ACTIVE` 时转为 `KEEP`。
9. 查询失败时保持 `PENDING` 并重试，不依据退款流水单独回收权益。
10. 每次权益变化追加 `entitlement_audit_logs`，不得覆盖历史审计记录。

### 6.4 当前权益查询

月卡权益由 `iap_subscriptions` 当前状态计算，不从交易数量或最新交易类型直接推导。

```sql
revoked_at IS NULL
AND status IN ('ACTIVE', 'CANCELED_ACTIVE')
AND expires_at > now()
```

用户打开 App 或收藏小铺时只读取后端聚合权益，不因页面访问启动订阅状态轮询。付费周期由
`expires_at` 自动失效；新的购买、续费、恢复、退款、撤销、过期和续费开关变化由 Huawei 关键事件
驱动并经 `QuerySubscription` 复核。用户主动点击“恢复购买”时可以触发用户级权威查询，但不得触发
应用级历史订单查询。

## 7. 生产对账任务

### 7.1 环境开关

```text
PRODUCTION: trade reconciliation enabled
SANDBOX:    trade reconciliation disabled
```

沙盒和 0 元订单不通过 `trade/orders/query` 验证补偿效果。

### 7.2 时间窗口

建议每天凌晨运行一次，只查询已经结束的完整自然日：

```text
window_start = 昨天 00:00:00.000（业务时区）
window_end   = 今天 00:00:00.000（开区间）
```

调用 Huawei API 时，如果接口使用闭区间时间戳，则将 `window_end` 转换为前一毫秒，
即“昨天 23:59:59.999”。内部统一使用 `[start, end)`，避免秒级边界重复或遗漏。

限制：

```text
单个窗口 <= 48 小时
仅查询最近 180 天
不查询当天未结束数据
```

180 天是 Provider 接口的硬边界，不是系统能够补齐的“完整历史”。超过 180 天且当时未通过
事件通知落库的订单，无法再通过 `trade/orders/query` 恢复，属于已知限制。因此事件通知原始记录
和已经落库的交易不得按 180 天周期删除。

每日任务可以重查最近两个完整自然日形成重叠窗口，重复结果由交易唯一键去重。
首次生产回补按 24 小时窗口从近到远执行，最远不超过 180 天。

两种任务使用独立进度：

| 任务 | `job_name` | 推进方向 | 用途 |
| --- | --- | --- | --- |
| 日常对账 | `trade_reconciliation` | 时间递增，每天处理最新完整自然日 | 持续补偿新订单 |
| 首次回补 | `trade_backfill` | 时间递减，从最近完整自然日回补至 180 天边界 | 首次上线建立有限历史 |
| 日常观察 | `trade_reconciliation_observe` | 时间递增 | 首次上线只记录真实响应摘要，不改业务表 |
| 回补观察 | `trade_backfill_observe` | 时间递减 | 验证历史分页和边界，不改业务表 |

四种任务不得读写彼此的 `window_start`、`window_end`、`continuation_token` 或完成状态。观察模式
切换为正式模式后，正式任务必须从自己的 checkpoint 开始，不能把“已观察”当成“已落库”。即使窗口
暂时重叠，也只通过交易幂等键合并结果，不能覆盖对方 checkpoint。每种任务还应使用各自的 advisory lock；如需限制 Huawei API 并发，再增加
Provider 级总锁或调度互斥，不能通过共用 checkpoint 实现互斥。

### 7.3 `continuationToken` 分页

每个时间窗口必须完整循环分页：

```text
continuationToken = empty

do:
    response = query(window_start, window_end, continuationToken)
    verify response
    transaction:
        upsert current page transactions
        save response.continuationToken and page_number
    continuationToken = response.continuationToken
while continuationToken is not empty

mark window completed
advance to next window
```

分页要求：

1. `trade/orders/query` 不提供 `pageSize` 参数，每页条数由 Huawei 决定，不能假定固定数量或只读取第一页。
2. 当前页全部落库成功后，才能保存下一页 token。
3. 网络失败、验签失败或数据库失败时，不得把窗口标记完成。
4. 正常失败重启后必须从保存的 `continuationToken` 精确续跑，不能默认从窗口起点重跑。
5. 整窗重跑依靠交易唯一键幂等，不能产生重复交易或重复权益变化。
6. 只有最后一页成功且不再返回 token，才推进窗口 checkpoint。

如果 Huawei 明确返回 continuation token 已失效，任务才允许把该窗口标记为“需要重新分页”，
记录原因后从窗口起点安全重跑；这属于异常恢复路径，不能替代正常的分页 checkpoint。

### 7.4 对账结果处理

`PURCHASE` 记录补齐购买交易和周期，但不因发现较新订单而撤销旧周期。

`REFUND` 记录写入退款交易并尝试关联原购买期。仅有财务退款而没有明确权益效果时，
必须设置 `PENDING`，再通过 `QuerySubscription` 判断当前订阅是否终止。

对账任务应记录以下指标：查询窗口数、页数、交易数、新增数、重复数、待归因退款数、
查询失败数、最大延迟和 checkpoint 滞后时间。

## 8. 数据库迁移策略

项目未上线，不保留当前测试数据，也不实现兼容层。

实施方式：

1. 直接修改现有建表迁移，使全新数据库一次创建目标结构。
2. 删除现有测试数据库卷或重建 `onebeat_test` 数据库。
3. 从版本 0 重新执行 `golang-migrate up`。
4. 不新增旧 `iap_orders` 到新交易表的回填 SQL。
5. 不保留旧字段读写分支，不进行双写。
6. 测试环境确认通过后，以同样方式初始化尚未承载业务数据的生产数据库。

需要重建的 IAP 表：

```text
iap_orders                       -> 删除，由 iap_provider_transactions 替代
iap_subscriptions                -> 按新结构重建
iap_subscription_tokens          -> 新建
iap_subscription_periods         -> 新建
iap_provider_transactions        -> 新建
iap_refund_requests              -> 新建
iap_reconciliation_checkpoints   -> 新建
iap_webhook_events               -> 按新约束重建
```

如果生产数据库在实施前已经产生真实订单，必须停止使用本节的破坏性方案并重新设计数据迁移；
在项目首次上线后，不得再修改已经发布执行过的迁移文件。

## 9. 代码改动步骤

### 阶段 A：数据结构和模型

1. 修改 `migrations/000002_create_store_domain.up.sql` 和对应 down migration。
2. 在 repository 中新增订阅 token、周期、Provider 交易、退款请求和 checkpoint 模型。
3. 将交易唯一键落实到 SQL `ON CONFLICT`，不要只依赖 Go 层先查后写。
4. 保证 token 只以哈希和密文保存，日志与 JSON 快照必须遮蔽原文。

### 阶段 B：订阅验证与恢复购买

1. `QuerySubscription(ctx, purchaseOrderID, purchaseToken)` 与 Harmony 服务端 API 一致；业务层用客户端/通知/JWS 中的购买单号，勿把 `subscriptionId`（商品 ID）当 `purchaseOrderId`。
2. 重写 `verifySubscription`，允许权威快照返回新 token 和新订单号。
3. 删除严格要求响应 token/order 与客户端输入完全相等的错误判断，改为验证订阅关系、商品、账号绑定和环境。
4. 删除 `persistAuthoritativeSubscription` 中订单号变化就撤销旧订单的逻辑。
5. 删除“自然到期映射为 `REFUNDED`”的逻辑。
6. 明确只有未到期的 `ACTIVE`、`CANCELED_ACTIVE` 提供权益；状态 3 归一为 `EXPIRED`，`BILLING_RECOVERY` 验证恢复为 `ACTIVE` 后才重新授权。
7. 为查询参数、重复恢复、token 变化、订单号变化、宽限期和自然到期分别增加测试。

### 阶段 C：通知和退款状态机

1. 通知先落库、再验签、最后执行业务事务。
2. 购买/续费通知写 PURCHASE 交易和周期。
3. 新增退款分类器和 `PENDING -> KEEP/REVOKE` 状态转换。
4. 新增 `iap_refund_requests` 匹配逻辑，识别服务端主动返费。
5. 新增 `EXPIRE` 主类型处理，将订阅和当前周期更新为 `EXPIRED`，但不生成退款流水。
6. 新增 `DID_CHANGE_RENEWAL_STATUS` 处理（`AUTO_RENEW_DISABLED` / `AUTO_RENEW_ENABLED`），更新 `auto_renewing` 与订阅状态，不生成交易流水。
7. 增加通知乱序、重复通知、退款查询失败、退款后仍 ACTIVE、`EXPIRE` 子类型和续费开关变更测试。

### 阶段 D：生产对账 worker

1. 在 Huawei IAP client 中实现 `trade/orders/query` 请求和响应模型。
2. 实现 48 小时以内的 `[start, end)` 时间窗口转换。
3. 实现 `continuationToken` 循环、页面事务和 checkpoint。
4. 为 `trade_reconciliation` 和 `trade_backfill` 建立独立 checkpoint、推进方向和 advisory lock。
5. 增加生产/沙盒开关；沙盒启动 worker 时直接跳过并记录原因。
6. 使用 fake client 覆盖多页、空页、重复页、第二页失败、token 失效、整窗重跑和两种任务交错运行。
7. 部署为独立 worker 进程或容器，避免每个 API 实例各自启动定时器。
8. 多实例时使用 PostgreSQL advisory lock，保证同一环境同一任务同时只有一个实例运行。

### 阶段 E：前端与接口

1. 前端恢复购买仍提交当前凭据，但不推断是否撤销。
2. 后端权益接口返回标准化月卡状态、`expiresAt` 和 `autoRenewing`。
3. `CANCELED_ACTIVE` 的展示文案应为“已取消续费，可使用至 YYYY-MM-DD”，不能显示为已失效。
4. `PENDING` 退款不向客户端展示内部状态；权益保持最近一次已确认结果，后台持续重试。

## 10. 测试与验收

### 10.1 单元和集成测试

必须覆盖：

1. `ACTIVE` 未到期时月卡有效。
2. `CANCELED_ACTIVE` 未到期时月卡有效。
3. `CANCELED_ACTIVE` 到期后月卡失效。
4. Huawei 状态 3 归一为 `EXPIRED`，不增加本地 `GRACE` 字段或状态。
5. `BILLING_RECOVERY` 后通过 `QuerySubscription` 确认恢复 `ACTIVE`，重新获得权益。
6. `QuerySubscription` 使用 `purchaseOrderId + purchaseToken`（与 `QueryOrder` 请求体字段相同，语义仍是订阅快照）。
7. token 和订单号同时变化时新增周期，不撤销旧交易。
8. 同一恢复购买请求执行两次不产生重复交易。
9. `USER_REFUND + ACTIVE/CANCELED_ACTIVE` 未到期时得到 `KEEP`。
10. `USER_REFUND + 状态 3/REVOKED/EXPIRED` 得到 `REVOKE`。
11. `RETURN_FEE` 保留权益。
12. 未归因退款保持 `PENDING`，查询失败不回收权益。
13. 自然到期不生成退款交易、不把购买交易改成退款。
14. 对账超过一页时读取全部 `continuationToken` 页面。
15. 第二页失败后 checkpoint 不越过失败页。
16. 重跑同一窗口不重复写交易和权益审计。
17. 日常对账推进 checkpoint 时不改变首次回补进度，反向亦然。
18. 自动续期订阅需要确认发货时只调用 `confirmSubscriptionPurchase`。
19. `BILLING_RETRY`、`VOLUNTARY`、`PRODUCT_NOT_FOR_SALE` 通知将订阅和周期更新为 `EXPIRED`，且不生成退款流水。
20. `AUTO_RENEW_DISABLED` 将未到期订阅更新为 `CANCELED_ACTIVE`，`AUTO_RENEW_ENABLED` 恢复 `ACTIVE`，且均不生成交易流水。

### 10.2 沙盒验收

1. 使用沙盒加速续费产生连续多期。
2. 核对每一期关键事件通知都被保存并验签。
3. 核对每一期产生一个 PURCHASE 交易和一个订阅周期。
4. 重复发送通知和重复点击恢复购买，记录数量保持不变。
5. 取消自动续费后，验证到期前状态为 `CANCELED_ACTIVE` 且权益仍有效。
6. 模拟 `QuerySubscription` 返回状态 3，验证归一为 `EXPIRED`、不生成退款流水且不建立额外状态字段。
7. 沙盒不以 `trade/orders/query` 返回结果作为验收项。

### 10.3 生产上线前验收

1. 对账 worker 先以只记录差异、不修改权益的观察模式运行。
2. 验证时间边界、分页、180 天限制和 checkpoint 恢复。
3. 使用真实非 0 元订单验证 PURCHASE 和 REFUND 查询。
4. 对账结果与事件通知、`QuerySubscription` 快照人工核对一致后，再允许自动补齐。

## 11. 完成标准

满足以下条件后，本次调整才算完成：

1. 数据库中每个首购、续费和退款都是独立交易。
2. token/order 变化不会再产生伪造的 `REVOKED` 记录。
3. `CANCELED_ACTIVE` 用户在到期前继续拥有月卡权益。
4. 付费周期到期后用户无权益；扣费恢复并经 `QuerySubscription` 验证为新一期 `ACTIVE` 后才恢复权益。
5. 用户退款只有在确认订阅终态后才回收权益。
6. 生产对账能够完整处理所有分页，并可从精确 checkpoint 恢复。
7. 文档和代码不把 180 天订单查询误认为完整历史。
8. 沙盒、生产和 fake client 测试边界清晰，不再用沙盒验证不可查询的补偿接口。
9. 所有重复通知、恢复购买和对账重跑均保持数据库与权益幂等。
10. 自动续期订阅使用正确的 `confirmSubscriptionPurchase` 接口确认发货。
11. 日常对账和首次回补使用独立 checkpoint，互不覆盖推进方向。

## 12. 参考资料与复核要求

- Huawei《Implementing the Subscription Process》：
  `https://developer.huawei.com/consumer/en/doc/harmonyos-guides-V14/iap-integrate-subscription-V14`
- Huawei IAP Kit REST API 目录中的订阅状态查询、应用购买记录相关支付订单查询、
  服务端通知记录查询和服务端关键事件通知。

Huawei 接口可能更新。实现 `trade/orders/query`、通知子类型和订阅状态映射时，必须以开发时
官方当前页面为准，并将实际请求/响应样例固化为脱敏测试 fixture。本文中的内部枚举属于
OneBeat 业务模型，不要求与 Huawei 原始字符串完全同名。
