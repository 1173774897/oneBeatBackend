# 收藏小铺系统设计

本文档定义 OneBeat「收藏小铺」第一版的前后端边界、华为服务接入方式、权益模型、API、
数据库表和运行时配置。后续实现应以本文档为准；如产品规则发生变化，应先更新本文档，
再修改代码和数据库迁移。

## 1. 目标与已确认规则

### 1.1 畅游月卡

- 通过 Huawei IAP Kit 销售按月自动续费的「畅游月卡」。
- 用户也可以在应用内输入活动口令，领取一个自然月的非自动续费月卡权益。
- 活动口令有两种账号使用规则：
  - `ONCE_PER_ACCOUNT`：每个华为账号最多成功兑换一次。
  - `UNLIMITED_PER_ACCOUNT`：同一华为账号可以重复兑换，直到活动总兑换上限耗尽。
- 两种活动都支持配置全局总兑换上限；不限制时使用 `null`，不要使用魔法大数字。
- 已有正在生效的 Huawei IAP 自动续费月卡时，不允许兑换口令，避免付费时间和赠送时间重叠。
- 畅游月卡生效期间可以使用所有角色、所有场景和 PRO 定制鼓机。
- 口令赠送只产生 OneBeat 业务权益，不创建 Huawei IAP 订单，也不会自动续费或扣款。

### 1.2 角色

| 角色 | `itemKey` | 默认免费 | 可单独购买 | 可限免 | 月卡包含 | 可重复选择 |
| --- | --- | --- | --- | --- | --- | --- |
| 火柴人 | `character.matchman` | 是 | 否 | 不需要 | 是 | 是 |
| 云行者 | `character.cloud` | 否 | 是 | 是 | 是 | 否 |
| 茶壶太太 | `character.teapot` | 否 | 是 | 是 | 是 | 否 |
| Wave | `character.wave` | 否 | 是 | 是 | 是 | 否 |
| 条纹步客 | `character.mismatch` | 否 | 是 | 是 | 是 | 否 |
| 思古特 | `character.scooter` | 否 | 是 | 是 | 是 | 否 |
| 轮滑小子 | `character.lilroll` | 否 | 是 | 是 | 是 | 否 |

轮滑小子不再限制为仅单人练习角色，单人和多人大厅使用同一角色池。

### 1.3 场景与 PRO 功能

| 内容 | `itemKey` | 默认免费 | 可单独购买 | 月卡包含 |
| --- | --- | --- | --- | --- |
| 夕阳海边 | `scene.sunset_coast` | 是 | 否 | 是 |
| 霓虹街口 | `scene.neon_street` | 否 | 是 | 是 |
| PRO 定制鼓机 | `feature.pro_drum_machine` | 否 | 否 | 是 |

应用首次启动和无有效付费权益时，默认场景必须是 `scene.sunset_coast`，不能默认选中收费的
霓虹街口。

### 1.4 多人模式权益

- 角色属于个人权益，每个队员只能选择自己可用的角色。
- 火柴人可以被多人重复选择，其他角色继续遵守队内唯一占用规则。
- 场景由队长选择，只校验队长的场景权益；队长可以使用霓虹街口时，全队跟随进入。
- PRO 定制鼓机由队长配置，只校验队长的 PRO 权益；队员跟随队长的最终节拍配置。
- 限免结束时不强制中断正在进行的行进。返回大厅、重新选择或再次出发时重新校验。

## 2. 华为服务与自有 API 的边界

### 2.1 使用的华为能力

数字内容应使用 Huawei IAP Kit，而不是通用 Payment Kit。IAP Kit 负责：

- AppGallery Connect 数字商品配置。
- 商品名称、地区价格和币种查询。
- 华为系统收银台。
- 非消耗型商品购买。
- 自动续费订阅购买、续费、取消和恢复。
- 已购商品查询和恢复购买。
- 服务端订单、订阅状态查询及确认发货。
- 续订、过期、退款和撤销等关键事件通知。

Account Kit 负责华为账号登录。客户端取得身份凭证后交给 OneBeat API 校验，后端以
**UnionID** 作为 OneBeat 用户的华为账号主标识。OpenID 是应用维度标识，只能作为登录过程中的
中间标识或诊断信息，不能作为 `users` 表的账号匹配键。这样，同一开发者账号下的多个 OneBeat
应用可以把同一个华为账号识别为同一用户。

在 AppGallery Connect 中必须为应用开通获取 UnionID 所需的 Account Kit 能力。第一版不使用
GroupUnionID；只有未来需要在不同开发者账号所属的应用之间共享用户身份时，才评估关联主体账号组
和 GroupUnionID。

官方资料：

- [IAP Kit](https://developer.huawei.com/consumer/cn/sdk/iap-kit)
- [Account Kit](https://developer.huawei.com/consumer/cn/sdk/account-kit)
- [Account Kit 术语：OpenID、UnionID 与 GroupUnionID](https://developer.huawei.com/consumer/cn/doc/doccenter-capabilities/account-glossary)
- [OpenID 和 UnionID 的格式与唯一性说明](https://developer.huawei.com/consumer/cn/doc/doccenter-atomic-service/account-guide-atomic-faq)
- [Account Kit 扩展能力：通过 OpenID 获取 UnionID](https://developer.huawei.com/consumer/cn/doc/doccenter-references/api/account-api-extend-function)
- [AppGallery Connect 数字商品配置](https://developer.huawei.com/consumer/en/doc/harmonyos-guides-V13/store-iap-product-agc-V13)
- [developerPayload 使用建议](https://developer.huawei.com/consumer/cn/doc/HMScore-Guides/key-parameters-0000001354668057)

### 2.2 OneBeat API 负责的能力

以下能力不能仅靠客户端安全完成，必须由 OneBeat API 负责：

- 校验 Account Kit 身份并签发 OneBeat 会话令牌。
- 对客户端返回的 IAP 购买数据进行服务端复核。
- 保存订单、订阅当前状态和退款/撤销状态。
- 接收并幂等处理华为关键事件通知。
- 兑换活动口令。
- 配置和判断角色限免窗口。
- 统一计算用户最终权益。
- 为客户端提供带服务端时间的权益快照。
- 记录兑换、订单和权益变更审计日志。

客户端的 `AppStorage` 只能作为界面缓存，不能继续作为“是否已购买”的权威来源。

## 3. 商品与权益目录

### 3.1 Huawei IAP 商品

建议在 AppGallery Connect 创建以下稳定商品 ID。商品 ID 创建后不应再按价格、版本或活动改名。

| OneBeat 权益 | Huawei 商品 ID | 商品类型 |
| --- | --- | --- |
| 畅游月卡 | `onebeat.pass.monthly` | 自动续费订阅 |
| 云行者 | `onebeat.character.cloud` | 非消耗型 |
| 茶壶太太 | `onebeat.character.teapot` | 非消耗型 |
| Wave | `onebeat.character.wave` | 非消耗型 |
| 条纹步客 | `onebeat.character.mismatch` | 非消耗型 |
| 思古特 | `onebeat.character.scooter` | 非消耗型 |
| 轮滑小子 | `onebeat.character.lilroll` | 非消耗型 |
| 霓虹街口 | `onebeat.scene.neon_street` | 非消耗型 |

火柴人、夕阳海边和 PRO 定制鼓机不创建独立 IAP 商品。客户端显示的实际价格必须来自 IAP
商品查询结果，不能在 ArkTS 或后端代码中写死人民币价格。

### 3.2 权益判定优先级

对角色、场景或功能的最终判断按以下顺序执行：

1. 内容是默认免费项：允许使用，原因 `DEFAULT_FREE`。
2. 用户存在有效畅游月卡：允许使用，原因 `PASS_IAP` 或 `PASS_REDEMPTION`。
3. 用户已购买对应非消耗型商品：允许使用，原因 `IAP_PURCHASE`。
4. 收费角色当前处于限免窗口：允许使用，原因 `LIMITED_FREE`。
5. 以上均不满足：拒绝使用，原因 `LOCKED`。

限免只适用于角色，不替代永久购买。限免到期后，未购买且无月卡的角色重新锁定。

## 4. 前端职责

### 4.1 商店展示

- 前端保存 `itemKey -> 名称/图片/页面样式` 的展示映射。
- 后端返回可销售商品 ID、访问状态、限免窗口和权益到期时间。
- 前端通过 IAP 查询商品名称、价格和币种，查询失败时显示“暂时无法获取价格”，不能显示旧的
  写死价格。
- “我的”页面展示永久购买、当前月卡覆盖和限免可用项，并明确区分来源。
- 月卡区域同时展示来源、到期时间和自动续费状态。
- 口令月卡应显示“赠送月卡，不会自动续费”。

### 4.2 登录时机

- 用户可以匿名浏览收藏小铺。
- 购买、恢复购买和兑换口令前必须完成华为账号登录。
- 应用启动或回到前台时优先尝试 Account Kit 静默登录；需要用户授权时再展示交互式登录。
- 华为账号切换或退出后，立即清除当前 OneBeat 会话和内存中的用户权益，再为新账号重新加载。

### 4.3 权益接入点

前端应新增统一 `EntitlementService`，至少覆盖：

- 收藏小铺购买按钮和“我的”列表。
- 单人/多人大厅角色选择。
- 队长的场景选择。
- 队长和单人模式的 PRO 定制鼓机入口。
- 从深链、历史页面或恢复状态直接进入受限页面的场景。

只隐藏按钮不构成权限校验。进入功能和提交选择时都必须经过统一服务判断。

### 4.4 本地缓存

- 默认免费内容始终离线可用。
- 已由后端验证的永久非消耗型权益可以持久化缓存，并在联网时主动刷新退款/撤销状态。
- IAP 月卡、口令月卡和限免必须保存明确 `endsAt`，离线时绝不能超过已知到期时间。
- 应用进程运行期间使用服务端时间与单调时钟推算当前时间，降低用户修改系统时间的影响。
- 无任何已验证缓存时，不应因为 IAP 或 API 请求失败而自动解锁收费内容。

## 5. 身份和安全绑定

### 5.1 Account Kit 登录

客户端使用 Account Kit 授权请求 `serviceauthcode` 权限，将返回的 ID Token 和一次性
`authorizationCode` 一并发送到 `POST /api/v1/auth/huawei`。后端必须先验证客户端 ID Token：

- JWT 签名和 `kid` 对应的华为公钥。
- `iss` 是否为预期发行方。
- `aud` 是否为 OneBeat 的 Client ID。
- `exp`、`iat` 等时间字段。
- 必需的用户主题标识是否存在。

验证成功后，后端按以下步骤取得可信 UnionID：

1. 使用 `HUAWEI_CLIENT_ID`、`HUAWEI_ACCOUNT_CLIENT_SECRET` 和 `authorizationCode` 调用华为 OAuth
   Token 接口。鸿蒙 Account Kit 的 `LoginWithHuaweiID` 授权码不传 `redirect_uri`。授权码只能使用一次。
2. 验证 OAuth 响应中的 ID Token，并要求其 `sub` 与客户端 ID Token 的 `sub` 完全一致。
3. 使用 OAuth 响应中的 Access Token 调用华为帐号用户信息接口，读取服务端返回的 UnionID。
4. 如果用户信息没有 UnionID，则只允许使用已通过签名验证的服务端 ID Token 中的 UnionID；两处都有值时
   必须一致。最终仍无法取得 UnionID，则登录失败，不能回退到 OpenID 创建另一用户。

客户端单独上传的 `unionId` 字符串不可信，不能直接用于登录。OpenID 和 UnionID 都严格区分
大小写；做摘要前必须保留华为返回的原始大小写，不做大小写或 Unicode 归一化。

数据库不保存 UnionID 明文，只保存：

```text
HMAC-SHA256(ACCOUNT_UNION_ID_PEPPER, huaweiUnionId)
```

`ACCOUNT_UNION_ID_PEPPER` 只能存在于服务器 secret 文件中。OpenID 第一版不落库；如果未来为了
迁移或故障审计确有需要，也只能保存单独加密或 HMAC 后的值，且不得参与用户唯一性判断。

应用转移到另一个华为开发者账号时 UnionID 可能变化。发生应用主体迁移前，必须先设计账号映射和
数据迁移流程，不能直接更换开发者账号后上线。

### 5.2 购买归属

后端为每个用户生成稳定、不可逆的购买绑定值：

```text
developerPayload = HMAC-SHA256(PURCHASE_BINDING_SECRET, oneBeatUserId)
```

客户端发起 IAP 购买时把该值传给华为。后端验证订单时必须比较 `developerPayload`，防止把一个
账号的订单提交给另一个账号领取权益。不要把业务订单号作为订阅 `developerPayload`。

## 6. 核心业务流程

### 6.1 初始化收藏小铺

1. 客户端尝试静默登录。
2. 客户端调用 `GET /api/v1/store/bootstrap`。
3. 后端返回目录版本、商品 ID、服务器时间、限免窗口、购买绑定值和当前权益。
4. 客户端使用返回的 Huawei 商品 ID 查询本地化商品信息。
5. 客户端渲染商店，并缓存本次权益快照。

### 6.2 购买非消耗型商品

1. 客户端确认用户已登录。
2. 客户端使用 IAP 拉起目标商品收银台，并携带稳定的 `developerPayload`。
3. IAP 返回购买数据、签名和签名算法。
4. 客户端调用 `POST /api/v1/iap/purchases/verify`。
5. 后端先验签，再调用华为服务端接口查询真实订单状态。
6. 后端校验应用、商品、订单状态、账号绑定和订单唯一性。
7. 在同一数据库事务中保存订单、创建永久权益并记录审计日志。
8. 持久化成功后调用华为确认发货接口。
9. 后端返回新的权益快照，客户端再解锁商品。

重复提交同一订单必须返回原有成功结果，不能创建第二份权益。

### 6.3 购买或续订畅游月卡

购买流程与非消耗型商品相同，但权益到期时间以华为查询结果为准：

- 用户关闭自动续费后，当期结束前仍然有效。
- 续费成功后更新订阅和月卡权益到期时间。
- 当前业务规则仅在 `lastSubscriptionStatus.status == 1` 且 `expiresTime > now` 时授予月卡权益。
  状态 `3`（尝试扣费/宽限）会被记录为 `GRACE`，但第一版不解锁收费内容。
- 过期、退款或撤销后关闭对应月卡权益。
- 关键事件通知丢失时，通过客户端恢复购买和定时对账补偿。

### 6.4 恢复购买

1. 客户端调用 IAP 已购商品/订阅查询。
2. 客户端将购买记录批量提交到 `POST /api/v1/iap/purchases/restore`。
3. 后端逐项向华为复核，不信任客户端声明的状态或到期时间。
4. 后端补齐遗漏订单、更新订阅并返回最终权益。

“恢复购买”不能再读取本机 `AppStorage` 后直接提示成功。

### 6.5 服务端关键事件通知

1. 华为向 `POST /api/v1/webhooks/huawei/iap` 发送关键事件。
2. 后端对原始请求体验签，先按事件唯一标识落库。
3. 已存在的事件直接返回成功，保证通知重试的幂等性。
4. 后端调用华为查询接口确认最新状态。
5. 更新订单/订阅/权益并写入审计日志。
6. 处理成功后标记事件完成；失败时保留错误和重试次数。

生产与沙盒通知分别指向生产和测试 API，不得混用数据库或密钥。

### 6.6 口令兑换

1. 客户端完成华为账号登录。
2. 客户端生成 UUID `idempotencyKey`，连同用户输入发送到兑换接口。
3. 后端规范化口令并计算 HMAC；全过程不得记录口令明文。
4. 匹配活动后检查开始时间、结束时间和启用状态。
5. 查询 Huawei IAP 月卡状态；`status == 1` 且 `expiresTime > now` 时拒绝兑换。这里包括正常续费，
   以及已取消自动续费但当前周期尚未结束的订阅；状态 `3` 不按当前规则授予月卡权益。
6. 在一个事务内锁定活动全局计数和账号计数。
7. 检查全局上限和 `ONCE_PER_ACCOUNT` 限制。
8. 计算自然月权益区间，创建兑换记录和权益记录。
9. 原子增加活动及账号计数，提交事务。
10. 返回新的权益快照。

`UNLIMITED_PER_ACCOUNT` 允许同一账号反复兑换并叠加月份，每次成功都消耗一次全局额度。
同一网络请求重试必须通过 `idempotencyKey` 返回第一次结果，不能再次增加月份。

## 7. 自然月定义

自然月不是固定 30 天，也不是从兑换日到当月月底，而是从生效时刻按月历顺延一个月：

- `1 月 15 日 10:00 -> 2 月 15 日 10:00`
- `1 月 31 日 10:00 -> 2 月 28 日 10:00`
- 闰年为 `1 月 31 日 10:00 -> 2 月 29 日 10:00`
- `3 月 31 日 10:00 -> 4 月 30 日 10:00`

到期区间使用左闭右开：

```text
startsAt <= now < endsAt
```

所有时间使用 `timestamptz` 以 UTC 保存，客户端转换成当地时间展示。月历运算由后端统一完成，
不能由客户端计算。

连续叠加时保留这一轮赠送权益的原始日期锚点。例如在 `1 月 31 日` 连续兑换三次：

```text
第 1 个月：1 月 31 日 -> 2 月 28/29 日
第 2 个月：2 月 28/29 日 -> 3 月 31 日
第 3 个月：3 月 31 日 -> 4 月 30 日
```

实现时不能直接对上一次截断后的 `endsAt` 调用普通月份加法，否则第二个月可能错误变为
`3 月 28 日`。兑换链需要保存 `chainAnchorAt` 和 `monthOrdinal`，每次都从原始锚点计算第 N 个
月的边界。权益过期后再次兑换则建立新的兑换链和新锚点。

## 8. API 设计

### 8.1 通用约定

- 所有接口只接受 HTTPS。
- 用户接口使用 `Authorization: Bearer <onebeat-token>`。
- 时间使用 RFC 3339 UTC 字符串。
- 成功响应沿用现有 envelope：`code = 0`、`message = "ok"`。
- 非成功响应同时使用正确 HTTP 状态码和稳定数字业务码。
- 日志记录 `requestId`、用户内部 ID 和活动/订单 ID，不记录 ID Token、purchase token 或口令。

```json
{
  "code": 0,
  "message": "ok",
  "data": {}
}
```

### 8.2 `POST /api/v1/auth/huawei`

用途：校验华为身份并签发 OneBeat 会话令牌。

客户端只提交 Account Kit 返回的 ID Token 和一次性授权码，不提交自报的 OpenID 或 UnionID。后端完成
客户端 ID Token 验证、授权码交换、服务端 ID Token 验证及 UnionID 获取后，使用 UnionID 摘要查找或
创建 OneBeat 用户。

请求：

```json
{
  "idToken": "<account-kit-id-token>",
  "authorizationCode": "<one-use-service-auth-code>"
}
```

响应：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "accessToken": "<onebeat-jwt>",
    "expiresAt": "2026-10-01T08:00:00Z",
    "user": {
      "id": "8ec0af56-96b6-4f82-a51e-980f0193eaea"
    }
  }
}
```

### 8.3 `GET /api/v1/store/bootstrap`

用途：一次取得目录、活动状态、购买绑定值和用户权益。匿名请求可以取得公开目录，但不返回
购买绑定值和账号权益。

响应核心结构：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "serverTime": "2026-09-29T08:00:00Z",
    "configVersion": "2026-09-29.1",
    "developerPayload": "<stable-user-binding>",
    "pass": {
      "active": true,
      "source": "PASS_REDEMPTION",
      "expiresAt": "2026-10-29T08:00:00Z",
      "autoRenewing": false
    },
    "items": [
      {
        "itemKey": "character.cloud",
        "kind": "CHARACTER",
        "huaweiProductId": "onebeat.character.cloud",
        "iapProductType": "NONCONSUMABLE",
        "access": {
          "allowed": true,
          "reason": "PASS_REDEMPTION",
          "validUntil": "2026-10-29T08:00:00Z"
        },
        "freeWindow": null
      }
    ]
  }
}
```

### 8.4 `POST /api/v1/iap/purchases/verify`

用途：验证一次购买结果并发放权益。

```json
{
  "idempotencyKey": "2542c749-41b8-42b2-b6bf-4720b9b938d5",
  "productId": "onebeat.character.cloud",
  "purchaseData": "<CreatePurchaseResult.purchaseData 返回的紧凑 JWS>"
}
```

HarmonyOS IAP Kit 4.1.0(11)+ 的 `purchaseData` 已经是包含签名的紧凑 JWS，不再另传旧版
`signature` / `signatureAlgorithm` 字段。后端必须先验证该 JWS，再从已验签的数据中提取订单号和
token，并调用华为服务端状态查询取得最终权威状态；不能接受客户端额外传入的用户 ID、支付状态、
金额或权益到期时间作为权威字段。

### 8.5 `POST /api/v1/iap/purchases/restore`

用途：批量恢复购买。请求使用 `purchases` 数组，每个元素字段与单笔验证一致。第一版建议限制
单次最多 50 条，超出后由客户端分页提交。

### 8.6 `POST /api/v1/store/redemptions`

请求：

```json
{
  "idempotencyKey": "dcad0178-00d2-4216-8f06-72078109f8fb",
  "code": "用户输入的中文口令"
}
```

成功响应：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "campaignKey": "campaign.autumn_2026",
    "redeemedAt": "2026-09-29T08:00:00Z",
    "grantStartsAt": "2026-09-29T08:00:00Z",
    "grantEndsAt": "2026-10-29T08:00:00Z",
    "pass": {
      "active": true,
      "source": "PASS_REDEMPTION",
      "expiresAt": "2026-10-29T08:00:00Z",
      "autoRenewing": false
    }
  }
}
```

### 8.7 `POST /api/v1/webhooks/huawei/iap`

该接口不使用 OneBeat 用户 Bearer Token，而是严格使用华为通知验签。必须基于未经重新序列化的
原始请求体验签。事件保存和状态更新应幂等，华为重发同一事件时返回成功。

### 8.8 建议错误码

| HTTP | 业务码 | 含义 | 客户端文案建议 |
| --- | --- | --- | --- |
| 400 | `40001` | 请求格式错误 | 请求有误，请稍后重试 |
| 401 | `40101` | OneBeat 登录无效 | 请先登录华为账号 |
| 503 | `50311` | 无法从 Account Kit 取得 UnionID | 华为账号服务暂时不可用，请稍后重试 |
| 409 | `40921` | 活动尚未开始 | 活动还未开始 |
| 409 | `40922` | 活动已结束或口令无效 | 口令无效或活动已结束 |
| 409 | `40923` | 账号兑换次数已满 | 该账号已经兑换过本活动 |
| 409 | `40924` | 活动总额度已用完 | 本次赠送已经领完 |
| 409 | `40925` | Huawei 月卡正在生效 | 当前月卡生效中，请在订阅结束后兑换 |
| 409 | `40926` | 幂等键内容冲突 | 请刷新页面后重试 |
| 422 | `42231` | IAP 订单无法验证 | 购买验证失败，请尝试恢复购买 |
| 422 | `42232` | 商品或账号绑定不匹配 | 订单与当前账号不匹配 |
| 503 | `50321` | Huawei IAP 暂时不可用 | 商店服务暂时不可用，请稍后重试 |

对于错误口令，不应返回“找到了哪个活动”等可用于枚举配置的内部信息。

## 9. 数据库设计

配置目录不进入数据库。数据库只保存身份、订单、订阅、权益、兑换和审计等已经发生的事实。
所有主键建议使用 UUID，所有业务时间使用 PostgreSQL `timestamptz`。

### 9.1 `users`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `huawei_union_id_hash` | `bytea` | 非空、唯一，`HMAC-SHA256` 结果，不保存 UnionID 明文 |
| `status` | `text` | `ACTIVE`、`DISABLED`、`DELETED` |
| `last_login_at` | `timestamptz` | 最近成功登录时间 |
| `created_at` | `timestamptz` | 非空 |
| `updated_at` | `timestamptz` | 非空 |

### 9.2 `iap_orders`

每笔 Huawei 订单一行，包含非消耗型首购和订阅续费订单。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `user_id` | `uuid` | 外键到 `users` |
| `item_key` | `text` | 下单时的 OneBeat 稳定权益 ID |
| `huawei_product_id` | `text` | 下单时的 Huawei 商品 ID |
| `product_type` | `text` | `NONCONSUMABLE`、`AUTORENEWABLE` |
| `huawei_order_id` | `text` | 非空、唯一，订单幂等键 |
| `original_order_id` | `text` | 订阅链首单标识，可为空 |
| `purchase_token_hash` | `bytea` | 查询索引，不记录可用 token 明文 |
| `purchase_token_ciphertext` | `bytea` | 使用服务端密钥加密，供后续向华为查询 |
| `developer_payload` | `text` | 购买账号绑定值 |
| `status` | `text` | `PENDING`、`PURCHASED`、`REFUNDED`、`REVOKED`、`FAILED` |
| `purchased_at` | `timestamptz` | 华为确认的购买时间 |
| `expires_at` | `timestamptz` | 订阅订单到期时间，永久商品为空 |
| `acknowledged_at` | `timestamptz` | 确认发货成功时间 |
| `verified_at` | `timestamptz` | 最近向华为验证时间 |
| `receipt_snapshot` | `jsonb` | 删除/遮蔽敏感 token 后的原始字段快照 |
| `created_at` / `updated_at` | `timestamptz` | 非空 |

`purchase_token_hash` 在订单表中只建索引，不强制唯一，因为同一订阅关系的不同续费订单是否复用
token 应以华为实际协议为准。订单唯一性以 `huawei_order_id` 为主。

### 9.3 `iap_subscriptions`

保存用户与自动续费商品之间的当前订阅关系。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `user_id` | `uuid` | 外键到 `users` |
| `item_key` | `text` | 固定为月卡权益键 |
| `huawei_product_id` | `text` | Huawei 月卡商品 ID |
| `subscription_key` | `text` | 华为订阅关系稳定标识，唯一 |
| `latest_order_id` | `uuid` | 外键到最新 `iap_orders` |
| `purchase_token_hash` | `bytea` | 查询索引 |
| `purchase_token_ciphertext` | `bytea` | 加密保存 |
| `status` | `text` | `ACTIVE`、`CANCELED_ACTIVE`、`GRACE`、`EXPIRED`、`REFUNDED`、`REVOKED` |
| `auto_renewing` | `boolean` | 是否继续自动续费，不单独决定当前权益 |
| `starts_at` | `timestamptz` | 当前关系开始时间 |
| `expires_at` | `timestamptz` | 当前已确认权益到期时间 |
| `verified_at` | `timestamptz` | 最近验证时间 |
| `created_at` / `updated_at` | `timestamptz` | 非空 |

### 9.4 `entitlement_grants`

统一记录用户获得的业务权益。默认免费和角色限免不写入本表，由目录配置实时计算。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `user_id` | `uuid` | 外键到 `users` |
| `entitlement_key` | `text` | 角色、场景或 `pass.all` |
| `source_type` | `text` | `IAP_NONCONSUMABLE`、`IAP_SUBSCRIPTION`、`REDEMPTION`、`ADMIN` |
| `source_ref` | `uuid` | 对应订单、订阅或兑换记录 ID |
| `starts_at` | `timestamptz` | 非空 |
| `ends_at` | `timestamptz` | 永久权益为空 |
| `revoked_at` | `timestamptz` | 未撤销为空 |
| `revoke_reason` | `text` | 退款、撤销或管理原因 |
| `created_at` / `updated_at` | `timestamptz` | 非空 |

唯一约束：`(source_type, source_ref, entitlement_key)`。常用索引：
`(user_id, entitlement_key, starts_at, ends_at)`，并为 `revoked_at IS NULL` 建部分索引。

### 9.5 `redemptions`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `user_id` | `uuid` | 外键到 `users` |
| `campaign_key` | `text` | 代码配置中的稳定活动 ID |
| `code_key` | `text` | 代码配置中的稳定口令 ID，不保存明文 |
| `config_version` | `text` | 兑换时使用的规则版本 |
| `idempotency_key` | `uuid` | 与 `user_id` 联合唯一 |
| `account_sequence` | `integer` | 该账号在活动内第几次成功兑换 |
| `chain_id` | `uuid` | 连续叠加月份所属兑换链 |
| `chain_anchor_at` | `timestamptz` | 兑换链原始日期锚点 |
| `month_ordinal` | `integer` | 此次兑换后累计到第几个月 |
| `redeemed_at` | `timestamptz` | 成功兑换时间 |
| `grant_starts_at` | `timestamptz` | 本次赠送区间开始 |
| `grant_ends_at` | `timestamptz` | 本次赠送区间结束 |
| `entitlement_grant_id` | `uuid` | 外键到 `entitlement_grants` |

需要检查约束：`grant_ends_at > grant_starts_at`、`month_ordinal > 0`、
`account_sequence > 0`。

### 9.6 `redemption_campaign_usage`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `campaign_key` | `text` | 主键，对应代码配置 ID |
| `redeemed_count` | `bigint` | 非负，活动全局成功次数 |
| `updated_at` | `timestamptz` | 非空 |

首次兑换时惰性创建。兑换事务通过 `SELECT ... FOR UPDATE` 锁定该行，检查代码配置中的
`maxTotalRedemptions` 后再递增。

### 9.7 `redemption_account_usage`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `campaign_key` | `text` | 联合主键 |
| `user_id` | `uuid` | 联合主键、外键到 `users` |
| `redeemed_count` | `integer` | 非负 |
| `last_redeemed_at` | `timestamptz` | 最近成功时间 |

`ONCE_PER_ACCOUNT` 在锁定本行后要求 `redeemed_count = 0`；无限模式不做账号次数上限检查。

### 9.8 `iap_webhook_events`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `uuid` | 主键 |
| `huawei_event_id` | `text` | 唯一，通知幂等键 |
| `environment` | `text` | `prod` 或 `test` |
| `event_type` | `text` | 华为事件类型 |
| `signature_valid` | `boolean` | 验签结果 |
| `payload` | `jsonb` | 遮蔽敏感字段后的事件快照 |
| `status` | `text` | `RECEIVED`、`PROCESSING`、`PROCESSED`、`FAILED` |
| `attempt_count` | `integer` | 处理次数 |
| `last_error` | `text` | 最近错误，不能包含 token |
| `received_at` / `processed_at` | `timestamptz` | 接收和完成时间 |

### 9.9 `entitlement_audit_logs`

记录 `GRANTED`、`EXTENDED`、`EXPIRED`、`REFUNDED`、`REVOKED` 和 `RESTORED`。至少包含
用户、权益键、来源、来源 ID、变更前后摘要、请求/事件 ID 和发生时间。审计记录只追加，不更新。

## 10. 配置设计

### 10.1 可以提交到公共仓库的配置

以下配置不是秘密，可以使用 Go 常量或只读结构体放在后端代码中：

- Account Kit Client ID、预期发行方、允许的受众和官方接口地址；测试与生产按实际应用配置隔离。
- `store_items` 的稳定 `itemKey`、类别、访问策略和是否被月卡包含。
- `iap_products` 的 Huawei 商品 ID 和商品类型。
- `redemption_campaigns` 的活动 ID、规则类型、赠送月数、开始/结束时间和全局上限。
- `redemption_codes` 的稳定 `codeKey`，但不包含口令明文或普通哈希。
- `free_access_windows` 的窗口 ID、角色 ID、开始/结束时间。

这些配置应统一包含 `configVersion`。服务启动时执行校验：

- `itemKey`、Huawei 商品 ID、活动 ID、口令 ID、窗口 ID 不重复。
- 结束时间晚于开始时间。
- 只有收费角色可以配置角色限免。
- 非消耗型商品不能映射到月卡，自动续费商品只能映射到 `pass.all`。
- 所有新增收费角色和场景默认 `includedInPass = true`。
- 活动口令 ID 必须能在私密摘要文件中找到。

配置错误时服务应启动失败，不能带着不完整目录对外提供购买服务。

### 10.2 不能提交到仓库的秘密

至少需要以下独立秘密，不能复用同一个值：

```text
ACCOUNT_UNION_ID_PEPPER
PURCHASE_BINDING_SECRET
PURCHASE_TOKEN_ENCRYPTION_KEY
REDEMPTION_CODE_PEPPER
ONEBEAT_JWT_SIGNING_KEY
HUAWEI_ACCOUNT_CLIENT_SECRET
HUAWEI_IAP_PRIVATE_KEY
HUAWEI_IAP_KEY_ID
HUAWEI_IAP_ISSUER_ID
```

后三项必须来自华为开发者联盟 **API Console 服务账号 JSON** 的 `private_key`、`key_id` 和
`sub_account`。服务账号 JWT 使用 `PS256`（RSA-PSS + SHA-256），因此 `private_key` 必须是 RSA
私钥；AGC 调试签名、IAP 通知验签或其他用途生成的 EC P-256 私钥不能替代它。服务启动时会解析私钥并
在密钥类型不正确时直接失败，避免带着错误鉴权配置上线。

中国区 IAP 服务地址按华为官方“公共说明”的站点表固定在代码中，不通过环境变量猜测：

```text
Order:        https://orders-drcn.iap.hicloud.com
Subscription: https://subscr-drcn.iap.hicloud.com
```

当前 HarmonyOS 接口路径分别使用 `/order/harmony/v1/...` 和 `/subscription/harmony/v1/...`。
若旧资料、示例或转述与官方页面冲突，以华为官方页面为准：

- [IAP 公共说明与站点信息](https://developer.huawei.com/consumer/cn/doc/HMSCore-References-V5/api-common-statement-0000001050986127-V5)
- [HarmonyOS 订单确认发货](https://developer.huawei.com/consumer/en/doc/harmonyos-references-V13/iap-confirm-purchase-for-order-V13)
- [基于 Service Account 开放鉴权](https://developer.huawei.com/consumer/cn/doc/hmscore-guides/open-platform-service-account-0000001053509221)

部署时优先使用只读文件挂载，而不是直接写进 Docker 环境变量，避免通过 `docker inspect` 暴露。
建议服务器目录：

```text
/opt/onebeatbackend/secrets/prod/
/opt/onebeatbackend/secrets/test/
```

生产与测试必须使用不同秘密。

### 10.3 口令保存方式

中文口令先执行 Unicode NFKC 规范化，再去除首尾空白；不要自动删除中间空格或改变大小写，
避免用户看到的口令和真实规则不一致。

```text
normalizedCode = TrimSpace(NFKC(input))
digest = HMAC-SHA256(REDEMPTION_CODE_PEPPER, normalizedCode)
```

摘要比较使用常量时间比较。不能使用无密钥的 MD5、SHA-1 或 SHA-256，因为“暹罗”这类短中文
口令可以被离线枚举。

建议另建不对外服务的管理命令，以隐藏输入方式读取口令并生成摘要。原始口令只保存在运营方的
密码管理器中。服务器私密文件只保存摘要和 pepper 版本，例如：

```json
{
  "pepperVersion": "prod-v1",
  "codes": {
    "code.autumn_2026": "<hex-encoded-hmac-sha256>"
  }
}
```

即使摘要文件也不建议提交到公共仓库。服务日志、请求日志和错误监控必须对 `code` 字段完全
丢弃，而不是只做部分遮盖。

共享口令无法阻止用户主动转发。HMAC 解决的是仓库和数据库泄露问题；账号登录、失败限流、
活动有效期和总兑换上限用于控制在线滥用。建议实际口令使用 4 至 8 个不常见汉字，不使用只有
两个汉字的常见词。

### 10.4 限流建议

- 单账号：10 分钟内最多 5 次失败尝试，24 小时最多 20 次失败尝试。
- 单 IP：10 分钟内最多 20 次失败尝试。
- 达到限制返回 HTTP `429`，但不透露口令是否存在。
- 成功兑换不计入失败次数，但仍受活动全局额度和账号规则限制。
- 第一版可使用进程内限流；多实例部署前应迁移到 Redis 或数据库限流。

## 11. 环境、迁移与运维

- `onebeat_prod` 和 `onebeat_test` 使用同一套 golang-migrate 文件，但数据完全隔离。
- 测试 API 只接受华为沙盒订单和沙盒通知；生产 API 只接受正式订单和正式通知。
- 商品目录配置在两个环境中保持相同 `itemKey`，Huawei 商品 ID 可以按华为后台实际要求分别配置。
- 数据库迁移只创建事实表，不插入目录、活动或限免配置行。
- 删除代码配置前要确认历史订单、兑换和审计仍能通过保存的 `item_key`、`campaign_key` 和
  `config_version` 正确解释。
- 活动规则修改必须提升 `configVersion`；已经成功的兑换永远使用兑换记录中的规则快照，不随新
  配置重新计算。
- 运行期定时任务应周期复核即将到期和长时间未验证的订阅，修复遗漏通知。

## 12. 实施顺序

1. 在 AppGallery Connect 创建沙盒商品并确认最终商品 ID。
2. 接入 Account Kit，开通 UnionID 能力，完成后端 ID Token 验证、UnionID 获取和 OneBeat 会话。
3. 建立代码目录配置和启动校验。
4. 新增数据库迁移及 repository/service 层。
5. 接入 IAP 商品查询、购买、服务端验证和确认发货。
6. 接入恢复购买和华为关键事件通知。
7. 实现权益聚合接口并替换前端本地 `AppStorage` 权威判断。
8. 实现口令摘要工具、私密挂载、兑换事务和限流。
9. 接入角色限免、场景权限、PRO 权限及多人规则。
10. 完成沙盒购买、续订、取消、退款、恢复、换机和并发兑换测试后再配置生产商品。

## 13. 开发就绪清单

本节按「本地可运行 → 沙盒联调 → 生产上线」整理。基础设施（本地 Postgres、Vultr 测试/生产 API、
HTTPS）见 [deployment.md](deployment.md)。目录、事实表、登录、会话、bootstrap、验单、恢复购买和
确认发货已落地；关键事件通知、定时对账、口令活动和限免活动仍是后续阶段。

### 13.1 当前仓库已具备

| 项 | 状态 |
| --- | --- |
| 业务规则、API 形状、表结构、配置边界 | 本文档 §1–§12 |
| PostgreSQL + 迁移工具链 | `deploy/docker-compose.database.yml`、`migrations/` |
| 本地 `go run` / Vultr 双环境部署 | `deployment.md` §本地开发环境 |
| 运行时秘密目录约定 | §10.2 `/opt/onebeatbackend/secrets/{prod,test}/` |
| Account Kit + OneBeat 会话 | `internal/auth/`、`POST /api/v1/auth/huawei` |
| IAP 服务端验单、恢复、确认发货 | `internal/huawei/`、`internal/store/`、`/api/v1/iap/purchases/*` |

### 13.2 华为侧：平台要开通什么、拿回什么

均在 [AppGallery Connect](https://developer.huawei.com/consumer/cn/service/josp/agc/index.html)（及 HarmonyOS
应用对应工程）完成。**测试与生产应用/凭证分开**，测试 API 只接沙盒订单与沙盒通知（§11）。

#### A. 应用与 Account Kit（登录，§5.1、§8.2）

| 在华为平台操作 | 拿回 / 记录到何处 | 用途 |
| --- | --- | --- |
| 创建/确认 HarmonyOS 应用，包名与客户端一致 | 应用 ID、包名 | 客户端与 `aud` 校验 |
| 开通 **Account Kit**，并开通 **UnionID** 能力（§2.1，必做） | — | 后端必须以 UnionID 建用户，不能只用 OpenID |
| Account Kit 凭据 | **Client ID** | 可进公共配置（§10.1）；JWT `aud` |
| Account Kit 凭据 | **Client Secret** | 服务器秘密 `HUAWEI_ACCOUNT_CLIENT_SECRET`（§10.2） |
| 确认 ID Token 的 `iss`、JWKS 地址 | 发行方、公钥 URL | 后端验签 |
| 客户端集成 Account Kit 授权登录并请求 `serviceauthcode` | 客户端拿到 **ID Token + 一次性授权码** | 调 `POST /api/v1/auth/huawei` |

后端不接受客户端自报 UnionID。后端用一次性授权码交换 Access Token/服务端 ID Token，再调帐号用户信息
接口取得 UnionID，完整绑定规则见 §5.1。

#### B. IAP Kit（商品与支付，§3.1、§6.2–§6.5）

使用 **IAP Kit** 数字商品，不要用 Payment Kit（§2.1）。

| 在华为平台操作 | 拿回 / 记录到何处 | 用途 |
| --- | --- | --- |
| 开通 **IAP Kit** / 应用内支付 | — | 收银台、服务端验单 |
| 按 §3.1 创建商品 ID（**先沙盒**） | 8 个稳定 **productId** | 与代码 `iap_products` 一致，创建后勿改名 |
| 配置订阅 `onebeat.pass.monthly`（自动续费） | 订阅组/续费规则 | 月卡 |
| 配置 7 个非消耗型商品 | 类型 = 非消耗型 | 角色、场景 |
| 配置沙盒测试账号 | 测试华为账号列表 | 沙盒购买 |
| 在 API Console 创建并下载服务账号 JSON | `private_key` → `HUAWEI_IAP_PRIVATE_KEY`；`key_id` → `HUAWEI_IAP_KEY_ID`；`sub_account` → `HUAWEI_IAP_ISSUER_ID` | PS256 服务端鉴权、查单、确认发货 |
| 配置 **关键事件通知** URL | — | 见下表 |

| 环境 | 通知 URL（HTTPS 公网可达） |
| --- | --- |
| 测试 | `https://<测试域名>:8443/api/v1/webhooks/huawei/iap` |
| 生产 | `https://<生产域名>/api/v1/webhooks/huawei/iap` |

本地 `localhost` 无法直接收华为回调；开发期可在 **Vultr staging** 联调 webhook，或临时隧道。
生产与测试通知、密钥、数据库 **不得混用**（§6.5、§11）。当前 webhook 路由尚未开放，配置通知地址
不会代替后端实现；接入事件 payload 前必须继续以对应的华为官方通知文档为准。

#### C. 客户端侧（与华为并行）

| 项 | 说明 |
| --- | --- |
| Account Kit + IAP Kit SDK | 登录、查价、购买、恢复购买（§4） |
| API Base URL | 本地 `https://localhost:8443`；联调 `staging` 测试域名 `:8443` |
| `itemKey` 展示映射 | 名称/图在客户端；价格 **仅** 来自 IAP 查询（§4.1） |
| `EntitlementService` | 统一权益入口（§4.3），不能仅靠隐藏按钮 |

### 13.3 自有后端：秘密与配置（不经过华为）

#### 必须生成的秘密（§10.2，prod/test 各一套，禁止复用）

| 秘密 | 生成方式建议 | 不落 Git |
| --- | --- | --- |
| `ACCOUNT_UNION_ID_PEPPER` | `openssl rand -hex 32` | 是 |
| `PURCHASE_BINDING_SECRET` | 同上 | 是 |
| `PURCHASE_TOKEN_ENCRYPTION_KEY` | 32 字节随机（AES 等，实现时定） | 是 |
| `REDEMPTION_CODE_PEPPER` | `openssl rand -hex 32` | 是 |
| `ONEBEAT_JWT_SIGNING_KEY` | 足够长的随机或 RSA 私钥 | 是 |
| `HUAWEI_ACCOUNT_CLIENT_SECRET` | 华为控制台 | 是 |
| `HUAWEI_IAP_PRIVATE_KEY` | API Console 服务账号 JSON 的 RSA `private_key` | 是 |
| `HUAWEI_IAP_KEY_ID` | 同一服务账号 JSON 的 `key_id` | 是 |
| `HUAWEI_IAP_ISSUER_ID` | 同一服务账号 JSON 的 `sub_account` | 是 |

挂载路径：`/opt/onebeatbackend/secrets/test/`、 `.../prod/`。仓库内布局与说明见
[deploy/secrets/README.md](../deploy/secrets/README.md)：单行秘密在 `test.env` / `prod.env`，
JSON 在 `secrets/test/`、`secrets/prod/` 下挂载为容器 `/run/secrets`。**不要**写进镜像。

#### 口令活动（§10.3，可晚于登录/IAP 联调）

| 项 | 说明 |
| --- | --- |
| 代码中配置 | `redemption_campaigns`、活动 ID、`ONCE_PER_ACCOUNT` / 上限、时间窗 |
| 口令明文 | 只存运营密码管理器；**不进仓库** |
| 服务器摘要文件 | `HMAC-SHA256(REDEMPTION_CODE_PEPPER, NFKC(口令))` → hex，按 `codeKey` 索引 |
| 管理命令 | 文档建议的隐藏输入生成摘要（§10.3） |

#### 可提交仓库的目录配置（§10.1，实现时加启动校验）

- `store_items`（§1.2–§1.3 的 `itemKey` 与策略）
- `iap_products`（与华为 productId 一一对应）
- `free_access_windows`（角色限免）
- `configVersion` 字符串

### 13.4 代码与数据：进入实现前建议顺序

与 §12 一致，并标明对外部依赖：

| 步骤 | 依赖华为凭证？ | 产出 |
| --- | --- | --- |
| 1. 沙盒商品 ID 在 AGC 创建并与 §3.1 对齐 | 是（仅 ID，可先不定价） | 商品列表冻结 |
| 2. 代码目录 + `configVersion` + 启动校验 | 否 | 可编译的 catalog 包 |
| 3. §9 数据库迁移 + repository | 否 | 表就绪 |
| 4. `POST /auth/huawei` + JWT | **Client ID/Secret + `serviceauthcode`** | 可登录并由服务端取得 UnionID |
| 5. `GET /store/bootstrap`、权益聚合 | 4 + catalog | 匿名/登录快照 |
| 6. IAP verify / restore / 发货 | **IAP 服务端凭据** | 沙盒购买闭环 |
| 7. Webhook + 对账 | **通知 URL + IAP 凭据 + 官方通知 payload 文档** | 续费/退款 |
| 8. 口令兑换 + 摘要文件 + 限流 | 摘要文件 + pepper | 活动月卡 |
| 9. 客户端 EntitlementService 接 API | 5–8 | 端到端 |

### 13.5 怎样算「可以进入开发阶段」

**最低门槛（今天就能开干后端/前端骨架）**

- 本地或 Vultr 测试库可连（`deployment.md` 本地开发环境）。
- §3.1 商品 ID 在 AGC **沙盒**已创建（或书面确认 ID 不再变更）。
- 已生成 test 环境用的 §10.2 秘密（华为 Secret 可后补；登录/IAP 联调前必须到位）。
- HarmonyOS 工程已能编译，并计划接入 Account Kit / IAP Kit。

**可以开始华为联调**

- Account Kit + UnionID 开通，Client ID/Secret 已给后端 test 环境。
- IAP 沙盒商品可查询价格，沙盒账号可下单。
- API Console 服务账号的 RSA `private_key`、`key_id`、`sub_account` 已配置在 test secrets。
- 测试域名 `:8443` 可访问，webhook 已指向 staging（或隧道）。

**可以上生产商品**

- §12 第 10 步：沙盒全场景测完后再在 AGC 配置正式商品与正式通知 URL。
- prod secrets 与 prod 库独立；与 test 密钥、webhook、Huawei 沙盒/正式环境严格隔离。

### 13.6 建议自检表（复制到迭代看板）

```text
[ ] AGC 应用 + 包名与客户端一致
[ ] Account Kit + UnionID 已开通
[ ] Client ID → 公共配置；Client Secret → secrets/test
[ ] IAP Kit 已开通；§3.1 八个 productId（沙盒）已创建
[ ] API Console 服务账号 RSA private_key/key_id/sub_account → secrets/test
[ ] Webhook → 测试 API 公网 URL
[ ] 沙盒测试华为账号已添加
[ ] test 环境 §10.2 全部秘密已生成并挂载
[ ] 口令摘要文件（若有活动）已生成，未提交 Git
[ ] 客户端 API 基址（本地 / staging）已配置
[ ] 后端：§9 迁移 + §8 API 按 §13.4 顺序实现
```

## 14. 当前实现进度

截至 `2026-09-30`，登录、目录和 IAP 主链路已落地：

- `migrations/000002_create_store_domain.*.sql` 已创建 §9 的全部事实表、约束和查询索引，并在本地
  `onebeat_test` 通过 golang-migrate 实际执行。
- `internal/store/catalog` 已写入稳定 `itemKey`、Huawei `productId`、默认免费策略和启动校验；当前
  没有活动口令，也没有角色限免窗口。
- `POST /api/v1/auth/huawei` 已实现客户端 ID Token 验证、一次性授权码交换、服务端 ID Token 验证、
  UnionID 获取和 OneBeat 会话签发；客户端不上传 UnionID。
- `GET /api/v1/store/bootstrap` 已支持匿名与登录状态。匿名只允许火柴人和夕阳海边；登录状态返回
  `developerPayload` 和聚合权益。
- `POST /api/v1/iap/purchases/verify` 与 `/restore` 已实现客户端 JWS 验签、华为服务端复核、应用/
  商品/环境/账号绑定校验、purchase token 加密落库、幂等权益写入和确认发货。
- HarmonyOS 前端已接入 Account Kit 授权登录、IAP 商品查询/购买/恢复、OneBeat 会话与统一权益缓存；
  旧版本本地模拟购买会迁移为安全默认值，不再被当成真实购买。
- 前端测试/生产 API 地址位于
  `entry/src/main/ets/store/StoreApiConfig.ets` 的 `STORE_API_BASE_URL_TEST` 和
  `STORE_API_BASE_URL_PROD`。两项均只接受 `https://` 地址，填写时不要带末尾 `/`。

自动化验证现状：后端 `go test ./...` 通过，前端 debug HAP 构建通过。

尚未实现且不能以本地假数据替代的闭环：华为关键事件通知、周期对账、口令兑换和活动限免。当前
没有配置活动口令或限免窗口，因此不影响先完成 Account Kit/IAP 沙盒购买联调。

联调前还有一个硬性配置检查：`HUAWEI_IAP_PRIVATE_KEY` 必须替换为 API Console 服务账号 JSON 中的
RSA 私钥。若填入 EC P-256 私钥，后端会报 `HUAWEI_IAP_PRIVATE_KEY is not an RSA key` 并拒绝启动。
