# 口令月卡与角色限免技术设计

状态：已实现（首期活动）  
日期：2026-10-01  
上位产品设计：[收藏小铺系统设计](store-system-design.md)

## 1. 目标与范围

本文定义 OneBeat 第一版口令月卡和角色限免的后端技术方案，作为数据库、配置、API、服务层、
客户端接入和测试的实现依据。

本期范围：

- 已登录用户通过活动口令领取一个生产自然月的非自动续费畅游月卡；非生产环境按测试时钟压缩为
  300 秒。
- 支持 `ONCE_PER_ACCOUNT` 与 `UNLIMITED_PER_ACCOUNT` 两种活动账号规则。
- 一个活动可以绑定多个不同口令；这些口令共享活动总额度和账号额度。
- 同一账号重复兑换时按当前环境的月边界连续叠加；生产环境不因短月丢失原始日期锚点。
- 生效中的赠送月卡禁止客户端发起 Huawei 自动续费月卡购买。
- 收费角色可以配置限免时间窗；当前限免对匿名和登录用户都生效。
- bootstrap 和最终权益判断统一反映口令月卡与限免结果。
- 口令摘要、限流、幂等、并发额度、审计与敏感信息保护。

### 1.1 首期运行配置（2026-10-01）

- 兑换活动：`campaign.zero_width_20261001`，北京时间 2026-10-01 00:00（含）至
  2026-10-02 00:00（不含），账号上限 1 次，不设全活动总额度；代码仅保存
  `code.zero_width_20261001`，口令明文不进入 Git。
- 角色限免：`character.lilroll` 使用同一时间窗口；只在窗口生效时返回给客户端，匿名用户同样获得
  `LIMITED_FREE` 权益。

本期不包含：

- 管理后台或运营可视化页面。
- 动态写数据库的活动和限免配置。
- 单个口令的独立次数上限；额度只按活动和账号统计。
- 未来限免预告。
- 口令赠送时长配置；每次成功兑换固定赠送一个环境月，生产为自然月，非生产为 300 秒。
- 将角色限免转成永久权益或写入 `entitlement_grants`。
- 多实例共享限流；当前部署保持单 API 实例，扩容前再迁移到 Redis 或数据库限流。

## 2. 已确认的业务规则

### 2.1 口令活动

1. 每次成功兑换固定赠送一个月 `pass.all`：生产环境为自然月，非生产环境为 300 秒测试月。
2. 一个 `campaignKey` 可以绑定多个 `codeKey`。
3. 同一活动下的所有口令共享：
   - `maxTotalRedemptions`；
   - `ONCE_PER_ACCOUNT` 或 `UNLIMITED_PER_ACCOUNT` 账号规则；
   - 活动开始、结束和启用状态。
4. 每个 `codeKey` 只能属于一个活动，口令摘要也必须全局唯一。
5. `ONCE_PER_ACCOUNT` 表示账号对整个活动只能成功兑换一次，而不是每个口令一次。
6. `UNLIMITED_PER_ACCOUNT` 允许同一账号跨同活动的不同口令重复兑换，直至活动总额度耗尽。
7. 用户已有生效中的 Huawei 自动续费月卡时，拒绝兑换。
8. 用户已有生效中的赠送月卡时，允许再次兑换并从当前赠送链末尾继续叠加。
9. 口令月卡不会创建 Huawei 订单、不会扣费，也不会自动续期。

### 2.2 Huawei 月卡互斥

赠送月卡生效期间，客户端不得发起 Huawei 自动续费月卡购买：

- 客户端展示月卡时读取 bootstrap 中的购买允许状态。
- 点击购买前必须重新刷新 bootstrap，不能只依赖旧缓存。
- `PASS_REDEMPTION` 生效时隐藏或禁用订阅购买入口，并解释“赠送月卡到期后可订阅”。

Huawei 收银台运行在客户端，旧客户端或异常路径仍可能绕过展示限制并完成真实扣款。真实订单已经
产生后，后端不能以“赠送月卡生效”为由拒绝验单，否则会形成已扣费但未发权益的状态。因此：

- 正常路径在购买前阻止；
- 若后端收到已经完成的合法 Huawei 订单，仍须验单、落库和授予 IAP 权益；
- 异常重叠时 `PASS_IAP` 作为当前月卡来源优先展示，不顺延或补偿原赠送时间；
- 记录不含敏感数据的告警和审计，供运营排查旧客户端或流程绕过。

这是客户端型 IAP 的技术边界，不应通过拒绝已支付订单来追求表面上的绝对互斥。

### 2.3 角色限免

1. 限免只适用于非默认免费的收费角色。
2. 当前时间位于限免窗口时，匿名和登录用户都可以使用该角色。
3. 未开始的未来窗口不通过 bootstrap 暴露，也不在客户端提前展示。
4. 时间窗使用左闭右开区间：`startsAt <= now < endsAt`。
5. 限免结束不强制中断已经开始的行进；返回大厅、重新选择或再次出发时重新校验。
6. 限免不创建用户权益记录，不替代永久购买；到期后自然恢复锁定。

## 3. 总体架构

```text
公开版本化配置
  ├─ 商品目录
  ├─ redemptionCampaigns ── codeKey 列表
  └─ freeAccessWindows
                 │
                 ├──────────────┐
                 ▼              ▼
私密口令摘要文件 + pepper     当前服务端时间
                 │              │
                 ▼              ▼
         RedemptionService   EntitlementAggregator
                 │              │
                 ├── Huawei 已知订阅复核
                 ├── PostgreSQL 事务/额度/兑换链
                 └── entitlement_grants + audit logs
                                │
                                ▼
                    GET /api/v1/store/bootstrap
```

配置描述“允许发生什么”，数据库只保存已经发生的兑换、计数和权益事实。活动与限免不建立运营配置表。

建议的代码边界：

```text
internal/store/catalog/
  items.go                  商品目录
  campaigns.go              公开口令活动配置
  free_windows.go           公开限免配置
  validate.go               全量启动校验

internal/store/redemption/
  codes.go                  摘要文件加载、规范化、常量时间匹配
  calendar.go               自然月边界算法
  limiter.go                进程内账号/IP 失败限流

internal/store/service/
  redemption.go             兑换编排与 Huawei 月卡互斥
  entitlement.go            最终权益聚合

internal/store/repository/
  redemption.go             兑换事务、计数、兑换链和幂等读取

cmd/redemption-code/
  main.go                    隐藏输入的摘要生成工具
```

## 4. 公开配置模型

### 4.1 口令活动

口令活动与商品目录一起编译进程序并共享全局 `ConfigVersion`：

```go
type RedemptionUsageRule string

const (
    OncePerAccount      RedemptionUsageRule = "ONCE_PER_ACCOUNT"
    UnlimitedPerAccount RedemptionUsageRule = "UNLIMITED_PER_ACCOUNT"
)

type RedemptionCampaign struct {
    CampaignKey        string
    CodeKeys           []string
    UsageRule          RedemptionUsageRule
    StartsAt           time.Time
    EndsAt             time.Time
    MaxTotalRedemptions *int64
    Enabled            bool
}
```

第一版不增加 `grantMonths`。每次成功兑换在业务代码中固定为一个环境月，避免配置与产品规则漂移。

示例仅展示结构，不代表真实活动：

```go
{
    CampaignKey: "campaign.autumn_2026",
    CodeKeys: []string{
        "code.autumn_2026.a",
        "code.autumn_2026.b",
    },
    UsageRule: OncePerAccount,
    StartsAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
    EndsAt:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
    MaxTotalRedemptions: int64Pointer(1000),
    Enabled: true,
}
```

稳定性规则：

- `campaignKey` 表示一轮额度和账号使用范围。需要重置计数时必须创建新 `campaignKey`。
- 只修改文案、时间或上限也必须提升全局 `ConfigVersion`。
- 活动历史仍在数据库中时，不复用旧 `campaignKey` 或 `codeKey` 表示其他活动。
- 同一活动增加新口令不会重置已使用额度。
- `maxTotalRedemptions = nil` 表示不设全局上限。

### 4.2 角色限免

```go
type FreeAccessWindow struct {
    WindowKey string
    ItemKey   string
    StartsAt  time.Time
    EndsAt    time.Time
    Enabled   bool
}
```

启动校验要求：

- `windowKey` 全局唯一；
- `itemKey` 必须存在、类型为 `CHARACTER`、不是默认免费角色且是可购买角色；
- `endsAt` 必须晚于 `startsAt`；
- 同一角色的两个启用窗口不得重叠，首尾相接允许；
- 所有配置时间必须显式使用 UTC。

不同角色的限免窗口可以重叠。

### 4.3 全量启动校验

服务启动时一次性验证商品、活动、口令引用和限免窗口。以下任一情况必须启动失败：

- 重复或空的 `campaignKey`、`codeKey`、`windowKey`；
- 活动没有口令、使用规则非法或时间范围非法；
- 全局上限小于或等于零；
- 同一个 `codeKey` 被多个活动引用；
- 公开配置引用的 `codeKey` 在私密摘要文件中不存在；
- 摘要不是 32 字节 HMAC-SHA256；
- 两个 `codeKey` 配置了相同摘要；
- 限免窗口引用不存在或不允许限免的商品；
- 同一角色存在重叠窗口。

私密文件允许暂时存在尚未被当前公开配置引用的额外 `codeKey`，以支持“先部署秘密、后部署代码”
的无中断发布顺序。服务只记录额外键名的告警，不输出摘要。

## 5. 口令秘密与摘要工具

### 5.1 规范化与摘要

```text
normalizedCode = TrimSpace(NFKC(input))
digest = HMAC-SHA256(REDEMPTION_CODE_PEPPER, normalizedCode)
```

- 不删除中间空格。
- 不转换大小写。
- 规范化后为空或超过 128 个 Unicode code point 时拒绝。
- HTTP 请求体仍受全局 JSON 大小限制；兑换接口应使用更小的局部字段限制。
- 摘要比较使用 `subtle.ConstantTimeCompare`。

一次请求只计算一次 HMAC，然后与所有已配置摘要做完整常量时间扫描；不要在找到首个匹配后提前
退出。活动和口令数量很小，固定扫描成本可以接受，并能避免明显的匹配时序差异。

### 5.2 私密文件

沿用现有格式：

```json
{
  "pepperVersion": "prod-v1",
  "codes": {
    "code.autumn_2026.a": "<64 个十六进制字符>",
    "code.autumn_2026.b": "<64 个十六进制字符>"
  }
}
```

新增运行配置：

```text
REDEMPTION_CODE_PEPPER
REDEMPTION_CODES_PATH
```

当公开配置没有任何活动时，这两项可以缺省，兑换功能和入口均关闭；只要存在活动配置，两项就必须
提供且通过启动校验。test 与 prod 使用不同 pepper、摘要文件和真实口令。

第一版一个进程只加载一个 pepper 版本。需要轮换 pepper 时采用以下顺序：

1. 生成新 pepper 和新摘要文件；
2. 在维护窗口内同步替换二者；
3. 重启服务完成原子切换；
4. 旧活动仍需接受旧口令时，不进行 pepper 轮换，或在后续版本扩展多版本 pepper 支持。

### 5.3 摘要生成命令

新增 `cmd/redemption-code`：

- 从终端隐藏输入口令，不接受命令行明文参数；
- 从文件或环境读取 pepper，但不输出 pepper；
- 输出 `codeKey`、`pepperVersion` 和 hex 摘要；
- 不写日志、不保存历史、不自动提交 Git；
- 支持 `--verify-file` 对摘要文件做格式与重复检查。

原始口令只保存于运营密码管理器。请求日志、中间件、错误监控和数据库均不得保存口令明文。

## 6. 数据库设计

现有 `000002_create_store_domain` 已包含所需事实表。已经执行过的迁移不得原地修改；实现时使用新的
迁移补充索引。

### 6.1 `redemptions`

每次成功兑换一行：

| 字段 | 含义 |
| --- | --- |
| `user_id` | 兑换账号 |
| `campaign_key` | 活动稳定 ID |
| `code_key` | 命中的稳定口令 ID，不是摘要或明文 |
| `config_version` | 兑换时的全局配置版本 |
| `idempotency_key` | 客户端 UUID，与用户联合唯一 |
| `account_sequence` | 该账号在该活动中的成功次数 |
| `chain_id` | 用户当前连续赠送月卡链 |
| `chain_anchor_at` | 该链第一月的原始时间锚点 |
| `month_ordinal` | 本次兑换完成后的月份序号，从 1 开始 |
| `redeemed_at` | 服务端确认成功时间 |
| `grant_starts_at` / `grant_ends_at` | 本次新增的一个生产自然月或非生产测试月区间 |
| `entitlement_grant_id` | 对应 `entitlement_grants` 行 |

`account_sequence` 按 `campaignKey + userId` 计数；`month_ordinal` 按用户当前赠送链计数。因此用户用
另一个活动的口令继续叠加时，账号活动序号可以重新从 1 开始，但赠送链月份序号继续增长。

建议新增：

```sql
CREATE INDEX redemptions_user_grant_end_idx
    ON redemptions(user_id, grant_ends_at DESC);
```

### 6.2 使用计数

- `redemption_campaign_usage`：保留为历史统计表，不参与在线额度判定。
- `redemption_account_usage`：一个活动与账号一行，所有口令共享账号次数。
- 配置全局上限时，在事务外按 `redemptions.campaign_key` 读取成功记录近似计数；不锁活动共享行。
- 并发越过阈值时允许少量超发，总额度是运营软上限，不是财务级硬约束。
- 成功事务才增加计数；失败、回滚和幂等重放不增加。
- 已成功兑换不会因活动配置后来缩小上限而撤销。

### 6.3 `entitlement_grants`

每次兑换创建一行：

```text
entitlement_key = pass.all
source_type     = REDEMPTION
source_ref      = redemptions.id
starts_at       = 本次 grant_starts_at
ends_at         = 本次 grant_ends_at
```

兑换和 grant 存在互相引用需求。事务开始后先在应用层生成 `redemptionID` 和 `grantID`：

1. 插入 `entitlement_grants(id=grantID, source_ref=redemptionID)`；
2. 插入 `redemptions(id=redemptionID, entitlement_grant_id=grantID)`；
3. 插入审计并更新计数；
4. 一次提交。

不把多次兑换合并为一行 grant。每次兑换保留独立事实，最终到期时间由聚合层计算。

### 6.4 审计

- 新兑换链第一笔：`event_type = GRANTED`。
- 活跃链上叠加：`event_type = EXTENDED`。
- `source_type = REDEMPTION`，`source_ref = redemption.id`。
- `request_id` 使用服务端请求 ID；幂等键保存在兑换记录，不在日志中重复记录用户输入。
- `before_snapshot` / `after_snapshot` 只保存月卡来源、链 ID 和到期时间等业务摘要。

限免是公开配置的实时判断，不为每个用户写权益或审计行。

## 7. 自然月与兑换链算法

### 7.1 时间基准

- 所有运算以服务端 `now.UTC()` 为准。
- 所有数据库时间用 `timestamptz`。
- 客户端只负责本地展示，不参与月界计算。
- 区间统一左闭右开。

环境由 `APP_ENV` 决定：`prod` 或 `production` 使用真实自然月；其他值（包括 `test`、`staging`、
`development`）启用测试时钟。测试时钟与 Huawei 沙盒保持同一换算概念：1 天为 10 秒，固定按
30 天计算一个兑换月，因此一次兑换有效 300 秒。活动自身的开始/结束时间不加速。

### 7.2 月边界函数

不能直接在上一次截断日期上连续调用 `AddDate`。定义：

```text
calendarBoundary(anchor, n):
    targetYearMonth = anchor 所在年月向后移动 n 个月
    targetDay = min(anchor.day, targetYearMonth 的总天数)
    保留 anchor 的 UTC 时、分、秒和纳秒
```

例如锚点为 `2027-01-31T02:00:00Z`：

```text
n=0  2027-01-31T02:00:00Z
n=1  2027-02-28T02:00:00Z
n=2  2027-03-31T02:00:00Z
n=3  2027-04-30T02:00:00Z
```

### 7.3 新链与叠加

在持有用户级事务锁后查找该用户尚未撤销且 `grant_ends_at > now` 的最新赠送记录：

- 不存在：建立新 `chain_id`，`anchor = now`，本次 `monthOrdinal = 1`；
- 存在：沿用其 `chain_id` 和 `chain_anchor_at`，本次 `monthOrdinal = maxOrdinal + 1`。

生产环境计算：

```text
grantStartsAt = calendarBoundary(chainAnchorAt, monthOrdinal - 1)
grantEndsAt   = calendarBoundary(chainAnchorAt, monthOrdinal)
```

非生产环境计算：

```text
grantStartsAt = chainAnchorAt + (monthOrdinal - 1) * 300 秒
grantEndsAt   = chainAnchorAt + monthOrdinal * 300 秒
```

如果历史数据存在不连续或重叠区间，事务拒绝继续叠加并报告需要人工修复，不能静默产生第二条活跃链。
启用测试时钟前已经创建的活跃自然月链不会被追溯缩短；为了保持已发权益不可变，该链继续使用自然月
边界，过期后建立的新链才使用 300 秒边界。

## 8. 兑换 API

### 8.1 路由

```text
POST /api/v1/store/redemptions
Authorization: Bearer <onebeat-token>
Content-Type: application/json
```

请求：

```json
{
  "idempotencyKey": "dcad0178-00d2-4216-8f06-72078109f8fb",
  "code": "用户输入的中文口令"
}
```

成功响应保留上位设计结构：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "campaignKey": "campaign.autumn_2026",
    "redeemedAt": "2026-10-01T08:00:00Z",
    "grantStartsAt": "2026-10-01T08:00:00Z",
    "grantEndsAt": "2026-11-01T08:00:00Z",
    "pass": {
      "active": true,
      "source": "PASS_REDEMPTION",
      "status": "ACTIVE",
      "expiresAt": "2026-11-01T08:00:00Z",
      "autoRenewing": false,
      "iapPurchaseAllowed": false,
      "iapPurchaseBlockedReason": "ACTIVE_REDEMPTION_PASS"
    }
  }
}
```

叠加兑换时，`grantStartsAt/grantEndsAt` 表示本次新增月份，`pass.expiresAt` 表示整条赠送链的最终
到期时间。

### 8.2 幂等规则

幂等范围为 `(user_id, idempotency_key)`：

- 相同幂等键、相同 `codeKey`：返回第一次成功结果，不重新检查活动是否已结束，也不增加额度；
- 相同幂等键、不同口令或无法匹配到原 `codeKey`：返回 `40926`；
- 失败请求不创建幂等成功记录，客户端修正输入后可以继续使用新幂等键；
- 并发相同请求由用户锁和唯一约束共同保证只有一次成功。

为了让历史成功可以重放，已使用活动对应的公开配置和私密摘要不能在客户端合理重试期内立即删除。

### 8.3 错误映射

| HTTP | 业务码 | 场景 |
| --- | --- | --- |
| 400 | `40001` | JSON、UUID 或字段格式非法 |
| 401 | `40101` | 会话无效 |
| 409 | `40921` | 已匹配口令，但活动尚未开始 |
| 409 | `40922` | 口令无效、活动关闭或已经结束 |
| 409 | `40923` | `ONCE_PER_ACCOUNT` 已使用 |
| 409 | `40924` | 活动总额度耗尽 |
| 409 | `40925` | Huawei 月卡仍在生效 |
| 409 | `40926` | 幂等键对应了不同输入 |
| 429 | `42901` | 失败尝试过多 |
| 503 | `50321` | Huawei 状态暂时无法复核 |
| 503 | `50301` | 数据库或商店服务不可用 |

错误响应不得包含 `campaignKey`、`codeKey`、摘要、匹配进度或用户输入。

## 9. 兑换处理流程

### 9.1 HTTP 层

1. 验证 OneBeat 会话并取得内部 `userID`。
2. 提取可信客户端 IP；当前直接 TLS 暴露部署使用 `RemoteAddr`。
3. 检查账号和 IP 限流状态。
4. 严格解析 UUID、code 长度和 JSON 尾随数据。
5. 调用 `RedemptionService.Redeem`。
6. 日志只记录请求 ID、用户 ID、结果码；成功时可记录 `campaignKey/codeKey/redemptionID`。

除非部署明确配置可信反向代理及其地址范围，否则忽略客户端提供的 `X-Forwarded-For`，避免伪造 IP
绕过限流。

### 9.2 服务层事务外步骤

1. NFKC 规范化并计算 HMAC。
2. 常量时间匹配 `codeKey`，再定位唯一活动。
3. 查询已成功的幂等记录；命中时按 §8.2 返回。
4. 检查活动启用状态与时间范围。
5. 若配置运营总额度，事务外读取当前成功记录近似计数；达到阈值返回 `40924`，并发时允许少量超发。
6. 对该用户数据库中已知的 Huawei 月卡 token 调用订阅状态查询并更新本地快照。
7. Huawei 不可用时失败关闭，返回 `50321`，不继续兑换。
8. 权威查询确认当前 Huawei 月卡有效时返回 `40925`。

Huawei 没有按 OneBeat 用户 ID 查询全部订阅的服务端接口，因此客户端在展示或提交口令前必须先执行
一次 IAP 恢复购买，把当前设备可见的订阅提交给后端。后端同时复核所有已保存的订阅 token。这样可
覆盖换机和历史订阅；不能仅凭客户端声明“没有订阅”。

### 9.3 数据库原子事务与一致性边界

每次兑换使用一个 `SERIALIZABLE` 事务原子写入兑换事实、权益、审计和账号计数。这里的
`SERIALIZABLE` 不是把整个活动或所有用户排成单队列：事务不读取或锁定活动共享计数行，不同账号的
兑换通常可以并发执行。兑换正确性主要依赖当前用户行锁；隔离级别用于兜底发现未预期的读写冲突。

兑换事务固定执行以下步骤：

1. `SELECT ... FOR UPDATE` 锁定当前 `users` 行，只串行化同一账号的兑换请求。该锁同时保护幂等复查、
   账号次数和赠送链追加，避免同一账号得到重复 `month_ordinal`；
2. 再次查询 `(user_id, idempotency_key)`。相同 `codeKey` 作为成功重放直接返回，不重复写入；不同
   `codeKey` 返回幂等冲突；
3. 重新读取本地 Huawei 月卡状态，缩小事务外复核与兑换提交之间的竞态窗口；
4. 惰性创建 `redemption_account_usage` 行，再以 `SELECT ... FOR UPDATE` 读取账号活动次数。由于已经
   持有用户行锁，这里不会形成跨账号或活动级热点；
5. 检查账号规则。当前实现保留精确账号次数，因为它复用赠送链本来就需要的用户锁，没有为次数限制
   新增共享锁；
6. 查询未撤销且尚未结束的赠送链，拒绝多个活跃链或不连续的链尾；
7. 在事务内生成兑换、grant 和链 UUID，并按原始锚点及当前环境时钟计算本次月边界；
8. 插入 `entitlement_grants`、`redemptions` 和 `entitlement_audit_logs`；
9. 原子增加该活动下当前账号的成功次数；
10. 提交事务。

只有 PostgreSQL `40001`（序列化失败）和 `40P01`（死锁）会有限重试整个事务，当前最多尝试 3 次。
唯一约束、业务冲突、上下文取消和其他数据库错误不做盲目重试。外部 Huawei 请求始终放在事务外，
避免网络等待长期占用数据库连接与行锁。

全局总额度属于软限制：服务层在进入事务前对 `redemptions.campaign_key` 做一次无锁计数。多个并发
请求可以读到相同计数并全部成功，所以阈值附近允许少量超发；事务内不再复查，也不更新或锁定
`redemption_campaign_usage`。这个取舍避免活动级热点，不影响同一账号幂等与赠送链的一致性。

### 9.4 跨 Huawei 购买的竞态边界

客户端购买拦截加上事务内本地状态复查可以覆盖正常路径，但无法原子锁住外部 Huawei 系统。极端情况
下，兑换提交后旧客户端仍可能立即完成 IAP 购买。处理原则遵循 §2.2：合法付费订单始终验收，记录
异常重叠，不撤回已经成功的赠送月份。

## 10. 失败限流

第一版使用进程内、带过期清理的滑动窗口限流器：

- 每账号：10 分钟最多 8 次失败；
- 每 IP：10 分钟最多 30 次失败；
- 成功兑换和成功幂等重放不计入；
- `5xx` 基础设施或 Huawei 故障不计入，避免外部故障锁死正常用户；
- 请求格式错误和无效/不可用口令计入失败；活动时间、账号次数、总额度、IAP 互斥和幂等冲突不计入；
- 达到任一限制统一返回 `42901`，不再进行口令匹配。

限流键不得使用华为 UnionID 明文；账号使用 OneBeat 内部 UUID。内存中只保存计数时间戳，不保存
输入口令。进程重启会清空第一版限流状态，这是已接受限制。扩展为多个 API 实例之前必须替换为共享
限流存储。

## 11. 权益聚合与 bootstrap

### 11.1 权益优先级

实现必须严格遵循：

1. `DEFAULT_FREE`
2. `PASS_IAP`
3. `PASS_REDEMPTION`
4. `IAP_PURCHASE`
5. `LIMITED_FREE`
6. `LOCKED`

上位设计把有效畅游月卡作为一层；这里进一步规定异常重叠时 `PASS_IAP` 优先于
`PASS_REDEMPTION`。当前代码按查询行覆盖 map，不能表达稳定优先级，实施时应改为收集各来源后由
纯函数按上述顺序决策。

新增访问原因：

```go
AccessLimitedFree = "LIMITED_FREE"
```

### 11.2 赠送链到期聚合

每次兑换各自创建一个月份 grant，bootstrap 不能只取“此刻 starts_at 已到”的单行结束时间，否则
连续兑换三个月只会显示第一个月到期时间。

对当前活跃赠送链：

- `active =` 存在未撤销且覆盖当前时刻的 grant；
- `expiresAt =` 同一 `chain_id` 所有未撤销连续月份的最大 `grant_ends_at`；
- `source = PASS_REDEMPTION`；
- `status = ACTIVE`；
- `autoRenewing = false`。

链中的未来月份只用于延长当前活跃 pass 的最终到期时间，不单独向用户展示为“未来权益”。

### 11.3 限免聚合

对每个角色在服务器当前时间查找生效窗口：

```text
window.Enabled && window.StartsAt <= now && now < window.EndsAt
```

命中时：

```json
{
  "access": {
    "allowed": true,
    "reason": "LIMITED_FREE",
    "validUntil": "2026-10-08T00:00:00Z"
  },
  "freeWindow": {
    "startsAt": "2026-10-01T00:00:00Z",
    "endsAt": "2026-10-08T00:00:00Z"
  }
}
```

如果更高优先级权益已允许访问，`access.reason` 使用更高优先级来源；`freeWindow` 仍可返回当前窗口，
供商店展示限免标识。未开始和已结束窗口统一返回 `freeWindow: null`。

匿名 bootstrap 也执行相同限免计算，但不返回账号权益或 `developerPayload`。

### 11.4 兑换入口与 IAP 购买状态

`redemption.available` 的含义是“当前至少存在一个启用且处于时间窗内的活动”，不是对某个未知口令
的额度或账号资格承诺。未来活动不使其变为 true。匿名用户也可看到兑换入口，提交时触发登录。

扩展 `pass`：

```json
{
  "iapPurchaseAllowed": false,
  "iapPurchaseBlockedReason": "ACTIVE_REDEMPTION_PASS"
}
```

- 生效的赠送月卡：不允许购买，原因为 `ACTIVE_REDEMPTION_PASS`；
- 生效的 IAP 月卡：不允许再次购买，原因为 `ACTIVE_IAP_SUBSCRIPTION`；
- 无生效月卡：允许购买，blocked reason 为空。

客户端不得自行根据时间或文案推导该字段。

## 12. 客户端接入要求

### 12.1 兑换

1. 展示兑换入口前读取 bootstrap；匿名用户点击后先登录。
2. 登录后先执行恢复购买并刷新 bootstrap。
3. 每次用户主动提交生成新的 UUID `idempotencyKey`。
4. 网络超时重试必须复用同一 UUID 和原输入。
5. 成功后用响应更新权益，再刷新一次 bootstrap 作为最终一致性确认。
6. 客户端日志和崩溃报告不得记录输入口令。

### 12.2 月卡购买

1. 进入购买页和点击购买按钮时都检查最新 `iapPurchaseAllowed`。
2. `ACTIVE_REDEMPTION_PASS` 时显示赠送到期时间和“到期后可订阅”。
3. 不允许通过深链、缓存页面或旧弹窗绕过统一 `EntitlementService`。
4. Huawei 已返回成功购买数据时仍必须提交后端验单，不能因本地赠送状态丢弃订单。

### 12.3 限免

- 匿名用户收到 `LIMITED_FREE` 即可选择角色。
- 缓存必须保存 `serverTime` 和 `validUntil`，使用单调时钟推算；不得超过服务端已知结束时间。
- 大厅选择、进入行进和再次出发都调用统一权益判断。
- 行进已经开始后不因本地计时到点立即中断。
- 客户端不展示未来窗口，因为 API 不下发未来窗口。

## 13. 安全与隐私

- 不在数据库、日志、指标、trace、错误信息或审计中保存口令明文。
- 不记录请求体；兑换路由要覆盖通用请求日志中可能出现的 body 捕获。
- 数据库只保存 `codeKey`，不保存摘要。
- pepper 与账号、购买绑定、JWT 等秘密独立。
- 摘要文件只读挂载，文件权限限制为部署用户可读。
- 口令比较使用常量时间；错误不返回候选活动或口令标识。
- 所有业务时间使用服务端时间，忽略客户端时间。
- 所有权益写入、额度增加和审计必须位于同一事务。
- API 只接受已认证的 OneBeat 用户兑换，不接受客户端传入 user ID。
- 幂等键只在用户范围内唯一，不能被另一个用户用来读取结果。

## 14. 可观测性

建议结构化日志字段：

```text
operation=redeem
request_id
user_id
result_code
campaign_key       # 仅成功匹配后，且不在无效口令日志中出现
code_key           # 仅成功日志
redemption_id      # 仅成功日志
account_sequence
month_ordinal
grant_ends_at
```

明确禁止：`code`、normalized code、digest、pepper、Huawei purchase token。

建议指标：

- 兑换成功数，按 `campaign_key`；
- 兑换业务失败数，按稳定业务码，不按输入值；
- 限流拒绝数，分账号/IP 类型；
- Huawei 复核失败数；
- 事务序列化重试和额度冲突数；
- IAP 与赠送月卡异常重叠数；
- 当前配置中活动和限免窗口数量。

不使用 `codeKey`、用户 ID 或 IP 作为高基数指标标签。

## 15. 测试方案

### 15.1 配置与秘密

- 重复活动、口令、窗口 ID 启动失败。
- 一个活动多个口令合法，跨活动复用口令失败。
- 缺少摘要、摘要格式错误、摘要重复启动失败。
- 额外秘密口令只告警、不失败。
- 非收费角色、场景、默认免费角色的限免配置失败。
- 同角色重叠窗口失败，首尾相接成功。

### 15.2 口令安全

- NFKC 等价输入匹配相同摘要。
- 只去除首尾空白，保留中间空格和大小写。
- 空值、超长值拒绝。
- 成功、失败、panic 和超时日志均不含输入或摘要。
- 常量时间扫描不会因命中位置提前退出。

### 15.3 自然月

- 1 月 15 日到 2 月 15 日。
- 平年 1 月 31 日到 2 月 28 日，再到 3 月 31 日。
- 闰年 1 月 31 日到 2 月 29 日，再到 3 月 31 日。
- 3 月 31 日到 4 月 30 日，再到 5 月 31 日。
- 年末跨年、纳秒保留和 UTC 一致性。
- 过期后重新兑换建立新锚点。
- `APP_ENV=prod/production` 仍使用自然月，其他环境一个兑换月严格为 300 秒。
- 非生产环境重复兑换按同一锚点生成连续的 300 秒区间。
- 测试时钟上线前的活跃自然月链不被缩短，后续月份仍保持原链 cadence。

### 15.4 幂等与并发集成测试

- 同一请求串行和并发重放只增加一次额度、一个月和一条审计。
- 相同幂等键不同口令返回 `40926`。
- 多个口令共享同一活动总额度。
- `ONCE_PER_ACCOUNT` 跨多个口令仍只能成功一次。
- `UNLIMITED_PER_ACCOUNT` 并发兑换生成连续且不同的月份序号。
- 同一用户跨两个活动并发兑换仍进入同一连续链。
- 总额度为 1 时，串行请求在首个成功后拒绝；多账号并发允许少量超发，且不会形成活动级锁等待。
- 事务失败不增加任何计数或残留 grant。

数据库并发测试必须使用真实 PostgreSQL，不能只用内存 fake。

### 15.5 Huawei 互斥

- 已知有效 IAP 月卡拒绝兑换。
- 已取消自动续费但当前周期有效时仍拒绝。
- 已过期或状态 3 时按现有业务规则允许兑换。
- Huawei 查询失败返回 `50321` 且不消耗额度。
- 赠送月卡生效时 bootstrap 禁止发起 IAP 购买。
- 异常收到合法付费订单时仍验单入账，并产生重叠告警。

### 15.6 限免与权益优先级

- 开始时刻生效，结束时刻立即失效。
- 匿名和登录用户得到一致的 `LIMITED_FREE`。
- 未来窗口不返回。
- 默认免费、IAP 月卡、赠送月卡、永久购买分别覆盖限免原因。
- 活跃赠送链叠加三个月时，bootstrap 返回链末最终到期时间。
- 限免结束后新的选择被拒绝，但已开始行进不被强退。

## 16. 发布与回滚

### 16.1 实施顺序

1. 增加配置模型、限免计算、启动校验和纯函数测试，保持活动列表为空。
2. 增加 `REDEMPTION_CODE_PEPPER`、摘要文件加载和摘要管理命令。
3. 新增索引迁移及 repository 兑换事务。
4. 增加 RedemptionService、限流器和 HTTP 路由。
5. 重构权益聚合，加入稳定优先级、赠送链最终到期和匿名限免。
6. 扩展 bootstrap 的 IAP 购买允许状态。
7. 客户端接入兑换、购买阻止和限免角色。
8. staging 配置测试活动和测试摘要，完成并发、换机恢复、月底叠加和离线到期测试。
9. 先部署 prod 摘要文件和 pepper，再部署引用这些 `codeKey` 的新配置版本。
10. 观察指标后启用正式活动或限免窗口。

### 16.2 无活动默认行为

活动和限免配置为空时：

- 现有购买、订阅、bootstrap 与 webhook 行为保持不变；
- `redemption.available = false`；
- 不要求部署口令秘密；
- 不出现 `LIMITED_FREE`；
- API 可以返回统一的活动不可用结果，或仅在功能启用后注册路由。建议始终注册路由并返回
  `40922`，减少客户端版本分支。

### 16.3 回滚

- 关闭活动使用 `Enabled=false` 并提升 `ConfigVersion`，不删除历史事实。
- 关闭限免不会撤销永久或月卡权益；客户端下一次刷新后恢复正常锁定。
- 已发放口令月卡不能通过删除配置回收。
- 回滚程序版本时必须确认旧版本能读取已有 redemption/grant 行；数据库新增索引无需回滚。
- 不手工减少 usage 计数或把成功 redemption 改成失败。确需补偿时使用单独的 `ADMIN` 权益和审计流程。

## 17. 验收标准

满足以下条件才视为功能完成：

1. 一个活动的多个口令共享总额度和账号规则。
2. 并发兑换不超发、不重复发月、不产生断裂月份。
3. 1 月 31 日等月底锚点连续叠加结果正确。
4. 有效 Huawei 月卡不能兑换，Huawei 故障时不误发权益。
5. 赠送月卡期间当前客户端无法进入 Huawei 订阅收银台。
6. 匿名用户可使用当前限免角色，未来窗口不泄露。
7. bootstrap 对叠加月份返回最终到期时间，且权益原因优先级稳定。
8. 口令明文和摘要不出现在 Git、数据库、日志、指标和错误响应中。
9. 失败限流、幂等重放、额度耗尽和账号限制均返回稳定业务码。
10. `go test ./...`、真实 PostgreSQL 并发测试和客户端端到端测试全部通过。
