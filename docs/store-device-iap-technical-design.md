# 收藏小铺：端侧购买与服务端兑换技术设计

状态：已实现（未上线），2026-11-03
适用范围：小铺的付费购买、恢复购买、口令兑换、账号注销，以及客户端如何合并这两类权益。
本文是现行方案的唯一依据。下列文档中与付费购买相关的内容已弃用，冲突时以本文为准：

- [收藏小铺系统设计](store-system-design.md)：其中 IAP 验单、订单落库、华为通知和对账的部分已弃用；商品目录、权益模型、自然月等仍然有效。
- [IAP 订单与对账方案调整](store-system-design-tweak.md)：整篇已弃用。
- [口令月卡与角色限免技术设计](redemption-free-access-technical-design.md)：其中兑换前向华为复核订阅（`40925` / `50321`）和服务端验单的部分已弃用，其余仍然有效。

## 1. 背景与目标

- 服务器部署在中国香港。境内用户的数据传到服务器属于个人信息出境。
- 购买记录（交易和消费记录）可能被认定为敏感个人信息。一旦被认定，人数再少也必须签订个人信息出境标准合同或通过认证。
- 目标：**购买数据完全不经过服务器**。出境数据只剩经不可逆处理的账号标识、兑换记录和兑换权益记录，适用"一年内不满 10 万人、不含敏感个人信息"的豁免。
- 合规材料：客户端仓库的 `docs/compliance/2026-11-03-store-cross-border-pia.md`（影响评估），以及三份隐私声明（`privacy.html`、`entry/src/main/resources/rawfile/legal/privacy.html`、`entry/src/main/ets/pages/PrivacyPage.ets`）。

已接受的代价：

- 付费权益在设备上判断。被修改过的客户端可以伪造已购状态；签名校验只能挡住伪造的购买数据，挡不住改过的程序。
- 服务器不再掌握收入和订单数据，改看华为 AppGallery Connect（AGC）后台报表。
- "华为自动续费月卡生效期间不能领取赠送月卡"只在客户端检查。被绕过时，后果只是赠送期和付费期重叠。

## 2. 职责边界

| 能力 | 客户端（HarmonyOS） | 华为 | 本服务（香港） |
|---|---|---|---|
| 商品价格、收银台 | 调用 IAP Kit | 提供 | 不参与 |
| 购买校验（JWS 验签） | **负责** | 签发 JWS | 不参与 |
| 确认发货 `finishPurchase` | **负责**（先保存后确认） | 记录 | 不参与 |
| 已购商品、月卡状态 | **负责**（每次完整查询华为） | 权威来源 | 不保存 |
| 退款、撤销、续订 | 下次查询时自然生效 | 权威来源 | 不接收通知 |
| 华为账号登录 | 仅用于兑换 | Account Kit | 校验并签发会话 |
| 口令兑换、赠送月卡 | 发起 | 不参与 | **负责** |
| 角色限免、商品目录 | 展示 | 不参与 | **负责**（匿名 bootstrap） |
| 账号注销 | 发起 | 不参与 | **负责** |

## 3. 客户端设计（oneBeat 仓库）

### 3.1 模块

| 文件 | 职责 |
|---|---|
| `store/IapJwsVerifier.ets` | 纯逻辑：解析 compact JWS、ES256 签名（原始 R‖S 转 DER）、信任策略、载荷解码、订阅状态映射。平台加解密通过 `IapJwsCrypto` 注入，可在 Node 中测试 |
| `store/IapJwsPlatformCrypto.ets` | 平台适配：`@kit.DeviceCertificateKit` 解析证书和校验证书链，`@kit.CryptoArchitectureKit` 的 `ECC256\|SHA256` 验签；内置根证书；调试包打印证书链 |
| `store/IapEntitlements.ets` | 查询华为、验签、生成并保存本地摘要、确认发货；对外提供摘要读取接口和变更通知 |
| `store/IapPurchaseService.ets` | `purchaseStoreProduct()`：拉起收银台，然后交给 `deliverNewPurchase()` |
| `store/EntitlementStore.ets` | `resolveItemAccess()` 统一合并服务器快照和本地付费摘要，发布到 AppStorage |
| `pages/StorePage.ets` | 购买、恢复购买、兑换、注销的界面流程 |

### 3.2 信任模型

1. 头部必须 `alg=ES256`，`typ` 为空或 `JWT`，`x5c` 非空。
2. 用 `x5c[0]`（叶子证书）的公钥校验签名。
3. 从**已签名的**载荷读取 `environment`：
   - `NORMAL`（或 `PRODUCTION`）：证书链必须校验到**随包内置的华为 IAP 根证书**（`HUAWEI_IAP_ROOT_CERTIFICATES_BASE64`）。列表为空时一律拒绝（失败关闭）。
   - `SANDBOX`：只校验叶子签名，与原后端一致；**只有调试包**（`BuildProfile.DEBUG`）接受。
   - 其他值：拒绝。
4. 载荷校验：`applicationId` 必须为 `6917617181108973504`；`packageName` 非空时必须为 `com.onebeat.app`；订单号和购买凭据不能为空；订阅的 `productType` 必须为 2，续期商品必须与当前商品一致。

由于环境值本身在签名内，自签名的 JWS 最多只能声称自己是 `SANDBOX`，而正式包会拒绝它。

**待办（上线前必须完成）**：正式根证书尚未写入。调试包在第一次购买或恢复购买时，会以 `[IapJws] x5c[i] subject=… issuer=… der#n=…` 的形式打印证书链（证书是公开数据，不打印载荷和凭据）。取得根证书并与华为公布的根证书核对后，写入 `IapJwsPlatformCrypto.ets`。

### 3.3 载荷映射

- 一次性商品（`jwsPurchaseOrder`）：有 `purchaseOrderRevocationReasonCode`（包括 0）或 `revocationTime > 0` 视为已退款或撤销，不计入已购。
- 自动续费月卡（`jwsSubscriptionStatus`，同一信封里同时有两种 JWS 时以它为准）：

| `status` | `autoRenewStatusCode` | 映射 |
|---|---|---|
| 1 | 1 | `ACTIVE` |
| 1 | 其他 | `CANCELED_ACTIVE`（已取消续费，本期有效） |
| 5 | 任意 | `REVOKED` |
| 其他 | 任意 | `EXPIRED` |

  订单被撤销时映射为 `REVOKED`。月卡有效的条件是：状态为 `ACTIVE` 或 `CANCELED_ACTIVE`，并且 `expiresTime` 晚于当前时间。多条记录取有效的那条；都有效或都无效时取到期时间最晚的。
- 数字字段同时接受数字和数字字符串（华为不同接口返回形式不同）。

### 3.4 本地摘要

- 保存位置：preferences `onebeat_iap_entitlements`，键 `state_v1`。
- 内容：`{ version, capturedAt, purchases[{productId, purchaseOrderId, purchaseTime, environment}], pass{productId, purchaseOrderId, status, expiresAt, autoRenewing, environment} | null }`。
- **不保存购买凭据**。`finishPurchase` 总是使用华为当次返回的凭据。
- 启动时用同步接口恢复，保证启动时校验所选场景和角色时已经能看到已购商品。
- 恢复时，如果当前是正式包，`SANDBOX` 条目会被丢弃。
- 不受服务器快照 30 天有效期的约束：已购商品一直有效，直到下一次成功的完整查询显示它不再存在；月卡在华为给出的到期时间结束。

### 3.5 查询与确认发货

完整刷新 `refreshLocalIapEntitlements()`：

1. 依次查询 `NONCONSUMABLE` 和 `AUTORENEWABLE`，每种都查 `CURRENT_ENTITLEMENT` 和 `UNFINISHED`。
2. 逐条验签；未通过的计入 `rejectedCount`，其余照常处理。
3. **任何一次查询失败，就放弃本次刷新，保留上一份摘要**，避免因为部分结果漏掉而误删已购商品。
4. 用本次结果整体替换摘要（退款、撤销的商品因此自然消失），写盘并 `flush`。
5. 写盘成功后，才对 `UNFINISHED` 中的订单调用 `finishPurchase`。确认失败只记日志，订单在华为仍是未完成状态，下次刷新会重试。

新购买 `deliverNewPurchase()`：验签，然后合并进摘要，写盘，再 `finishPurchase`。商品与所选商品不一致时报错。

顺序不能反过来：如果先确认发货、后写盘，写盘失败会造成"华为已确认、应用却没有发放"。

刷新和新购买通过同一个队列串行执行，所以先列出的完整查询不会覆盖刚完成的购买。

触发时机：

| 时机 | 方式 |
|---|---|
| 启动和回到前台 | `refreshLocalIapEntitlementsIfStale()`，距上次成功查询不足 10 分钟就跳过（`EntryAbility.IAP_FOREGROUND_REFRESH_MS`） |
| 每次打开小铺 | 立即查询，不阻塞页面 |
| 点「恢复购买」 | 立即查询；提示「已购权益已恢复」「当前华为账号没有可恢复的购买」或「部分购买记录未能核验」 |
| 兑换前 | 立即查询（失败时使用已保存的摘要），用于月卡互斥检查 |
| 华为返回"已拥有" | 立即查询后同步 |

购买和恢复购买**不需要登录小铺账号，也不需要跨境同意**。

### 3.6 权益合并（`EntitlementStore.resolveItemAccess`）

优先级与服务端 `catalog.itemSnapshots` 一致：

1. 服务器快照中的 `DEFAULT_FREE`；
2. 本地已购（`IAP_PURCHASE`，永久）；
3. 华为月卡（`PASS_IAP`，到华为给出的到期时间为止），只覆盖月卡包含的商品；
4. 服务器快照中的其他有效权益（赠送月卡 `PASS_REDEMPTION`、限免 `LIMITED_FREE` 等），受 30 天快照有效期限制。

补充规则：

- 没有服务器快照时，商品编号到 `itemKey` 的映射和月卡包含的商品使用客户端内置表（`IapEntitlements.PRODUCT_ITEM_KEYS`、`PASS_INCLUDED_ITEM_KEYS`），必须与 `internal/store/catalog` 保持一致。
- 华为月卡和赠送月卡同时有效时，月卡来源显示为 `PASS_IAP`。
- 任一月卡有效时，`storePassIapPurchaseAllowed=false`，不能再买华为月卡。服务器快照有效期内，它返回的 `iapPurchaseAllowed=false` 也会生效。
- 华为月卡有效时，客户端拒绝兑换赠送月卡，提示「华为自动续费月卡仍在生效，暂时不能领取赠送月卡」。
- 华为月卡有效即视为"会员状态已知"，供分享打卡奖励判断使用。

## 4. 服务端设计（本仓库）

### 4.1 接口

| 接口 | 鉴权 | 说明 |
|---|---|---|
| `POST /api/v1/auth/huawei` | 无 | 校验华为凭证，签发会话。只在兑换和注销时调用 |
| `GET /api/v1/store/bootstrap` | 可选 | 匿名：目录、限免、兑换入口状态。登录：另含兑换获得的权益和赠送月卡。已注销账号的会话返回 401 |
| `POST /api/v1/store/redemptions` | 必需 | 口令兑换。不再向华为复核订阅，所以没有 `40925`、`50321` |
| `POST /api/v1/account/deletion` | 必需 | 注销，见 §4.3 |

已删除：`/api/v1/iap/purchases/verify`、`/api/v1/iap/purchases/restore`、`/api/v1/webhooks/huawei/iap`，访问时返回 404（`TestNoPurchaseRoutesRemain`）。错误码 `40925`、`40931`、`40932`、`50321` 已删除。`bootstrap` 不再返回 `developerPayload`，客户端下单时也不设置它。

### 4.2 权益来源

- `entitlement_grants.source_type` 只允许 `REDEMPTION` 和 `ADMIN`。
- `repository.Bootstrap`：`REDEMPTION` 来源映射为 `PASS_REDEMPTION`，`ADMIN` 来源映射为 `IAP_PURCHASE`；赠送月卡链由兑换记录计算。
- 兑换事务锁住 `users` 行，并校验账号状态为 `ACTIVE`，防止"鉴权通过之后账号才被注销"的竞态。

### 4.3 注销

请求：`POST /api/v1/account/deletion`，需要 `Authorization: Bearer`，请求体为空。

| HTTP | code | 含义 |
|---|---|---|
| 200 | 0 | `data.deletedAt`。账号已注销时重复调用仍返回最初的注销时间（幂等，客户端没收到首次响应时可以安全重试） |
| 401 | 40101 | 凭证无效，或账号不存在 |
| 503 | 50301 | 存储故障 |

数据处理（`repository.DeleteAccount`，单事务，锁 `users` 行）：

- `users`：`status='DELETED'`、`deleted_at=now`；`huawei_union_id_hash` 替换为 `gen_random_bytes(32)`，切断与华为账号的对应关系。同一华为账号再次登录会创建新账号。
- `entitlement_grants`：撤销仍然有效的权益（`revoke_reason='ACCOUNT_DELETED'`），并写入 `entitlement_audit_logs`（`REVOKED` / `ACCOUNT_DELETION`）。
- **兑换记录不改动**，留在已注销账号名下。由于没有保留任何华为账号标识，同一华为账号注销后重新登录，可以再兑换一次"每账号一次"的活动。这是 2026-11-03 明确接受的取舍。
- 已购商品、华为月卡和自动续费都属于华为账号，不受注销影响。

会话：JWT 无状态。`requireUser` 和 `bootstrap` 每次按主键检查 `users.status`，非 `ACTIVE` 一律返回 401。只有注销接口本身接受已注销账号的凭证，以实现幂等。

客户端：入口在小铺「我的」页底部，两步确认。弹窗说明只会失去兑换获得的权益；已购商品随华为账号保留；注销不会停止自动续费。注销成功后清除会话和跨境同意，再用匿名 bootstrap 重置服务器部分的权益。

## 5. 数据库设计

PostgreSQL 17，逻辑库 `onebeat_test` / `onebeat_prod`，迁移工具 golang-migrate（`migrate/migrate:v4.19.1`）。全部结构只由两个迁移建立：`000001_create_store_metadata` 和 `000002_create_store_domain`。下文取自在全新临时库上执行这两个迁移后的 `pg_dump --schema-only` 结果。

### 5.1 设计原则

- **不保存任何购买数据**：没有订单、订阅、购买凭据、金额或华为通知相关的表。`entitlement_grants.source_type` 的检查约束只允许 `REDEMPTION` 和 `ADMIN`，从数据库层面挡住购买权益被写回。
- **不保存可直接识别身份的信息**：华为账号只以 UnionID 的 HMAC 摘要保存；兑换码只保存 `code_key`（配置里的口令编号），不保存明文；不保存昵称、IP 和设备信息。
- **注销后只留去标识化历史**：注销把 `huawei_union_id_hash` 换成随机值，所有行仍然挂在原 `users.id` 下，但这个 id 再也对应不到华为账号。
- 所有外键都指向 `users(id)` 或 `entitlement_grants(id)`，不使用级联删除。注销不物理删除行，见 §4.3。

### 5.2 关系

```
users ─┬─< entitlement_grants ─┬─ 1:1 ─ redemptions (entitlement_grant_id)
       │                       └─ source_ref = redemptions.id（REDEMPTION 来源）
       ├─< redemptions
       ├─< redemption_account_usage  (campaign_key, user_id)
       └─< entitlement_audit_logs    (source_ref = redemptions.id 等)
store_metadata（独立）
```

### 5.3 表结构

#### `users`：小铺账号

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `id` | uuid | PK，默认 `gen_random_uuid()` | 会话 JWT 的 `sub` |
| `huawei_union_id_hash` | bytea | NOT NULL，UNIQUE | `HMAC-SHA256(ACCOUNT_UNION_ID_PEPPER, UnionID)`，pepper 只在服务器密钥文件中。注销后替换为 `gen_random_bytes(32)` |
| `status` | text | 默认 `ACTIVE`；只允许 `ACTIVE` / `DISABLED` / `DELETED` | `requireUser`、`bootstrap`、兑换事务都会检查，非 `ACTIVE` 视为无效登录 |
| `last_login_at` | timestamptz | NOT NULL | 每次登录更新 |
| `created_at` / `updated_at` | timestamptz | 默认 `now()` | |
| `deleted_at` | timestamptz | 可空 | 注销时间；约束 `users_deleted_at_matches_status`：`(status='DELETED') = (deleted_at IS NOT NULL)` |

登录 `UpsertUser`：`INSERT … ON CONFLICT (huawei_union_id_hash) DO UPDATE … WHERE users.status = 'ACTIVE'`。注销后摘要已经换成随机值，同一华为账号再次登录不会冲突，会插入一个新账号。

#### `entitlement_grants`：服务器发放的权益

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `id` | uuid | PK | |
| `user_id` | uuid | FK → `users` | |
| `entitlement_key` | text | NOT NULL | 兑换固定为 `pass.all`；`ADMIN` 可以是具体 `itemKey` |
| `source_type` | text | 只允许 `REDEMPTION` / `ADMIN` | `REDEMPTION` 在 bootstrap 中映射为 `PASS_REDEMPTION`，`ADMIN` 映射为 `IAP_PURCHASE` |
| `source_ref` | uuid | NOT NULL | `REDEMPTION` 时为 `redemptions.id` |
| `starts_at` / `ends_at` | timestamptz | `ends_at` 可空（永久），否则必须晚于 `starts_at` | 生效区间，左闭右开 |
| `revoked_at` / `revoke_reason` | timestamptz / text | 有原因时必须有撤销时间 | 注销时为 `ACCOUNT_DELETED` |
| `created_at` / `updated_at` | timestamptz | | |

唯一约束 `(source_type, source_ref, entitlement_key)`，防止同一来源重复发放。

#### `redemptions`：成功的兑换

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `id` | uuid | PK | 由事务内 `gen_random_uuid()` 生成，同时作为权益的 `source_ref` |
| `user_id` | uuid | FK → `users` | |
| `campaign_key` | text | NOT NULL | 活动编号（配置） |
| `code_key` | text | NOT NULL | 命中的口令编号（配置），不是口令明文 |
| `config_version` | text | NOT NULL | 兑换时的配置版本 |
| `idempotency_key` | uuid | UNIQUE `(user_id, idempotency_key)` | 客户端重试复用同一个键 |
| `account_sequence` | integer | > 0 | 该账号在该活动中的第几次兑换 |
| `chain_id` / `chain_anchor_at` / `month_ordinal` | uuid / timestamptz / integer | `month_ordinal > 0` | 赠送月链：同一条链按锚点顺延自然月，见《口令月卡与角色限免技术设计》§7 |
| `redeemed_at` | timestamptz | | |
| `grant_starts_at` / `grant_ends_at` | timestamptz | 结束时间必须晚于开始时间 | 本次兑换赠送的区间 |
| `entitlement_grant_id` | uuid | FK → `entitlement_grants`，UNIQUE | 与权益一一对应 |
| `created_at` | timestamptz | | |

#### `redemption_account_usage`：每账号每活动的兑换次数

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `campaign_key` | text | PK 第 1 列 | |
| `user_id` | uuid | PK 第 2 列，FK → `users` | |
| `redeemed_count` | integer | 默认 0，≥ 0 | `ONCE_PER_ACCOUNT` 活动据此拒绝第二次兑换 |
| `last_redeemed_at` | timestamptz | 可空 | |

兑换事务先 `INSERT … ON CONFLICT DO NOTHING`，再 `SELECT … FOR UPDATE` 锁住这一行，以串行化同一账号的并发兑换。注销时不改这张表（§4.3 的取舍）。

#### `entitlement_audit_logs`：权益变动审计

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `id` | uuid | PK | |
| `user_id` | uuid | FK → `users` | |
| `entitlement_key` | text | NOT NULL | |
| `event_type` | text | 只允许 `GRANTED` / `EXTENDED` / `REVOKED` | 兑换写 `GRANTED`（新链）或 `EXTENDED`（续上已有的链），注销写 `REVOKED` |
| `source_type` | text | NOT NULL | `REDEMPTION` 或 `ACCOUNT_DELETION` |
| `source_ref` | uuid | 可空 | 兑换记录 id |
| `before_snapshot` / `after_snapshot` | jsonb | 可空 | 只含 `source`、`chainId`、`expiresAt`，不含口令或身份信息 |
| `request_id` | text | 可空 | 服务端请求编号，便于和日志对应 |
| `occurred_at` | timestamptz | 默认 `now()` | |

#### `store_metadata`

键值表，`000001` 写入 `schema_initialized=true`，业务代码不读写。`schema_migrations` 由 golang-migrate 维护。

### 5.4 查询与索引

| 查询（代码位置） | 条件 | 使用的索引 |
|---|---|---|
| 会话状态校验 `UserStatus` | `id = $1` | `users_pkey` |
| 登录 `UpsertUser` | `huawei_union_id_hash` 冲突 | `users_huawei_union_id_hash_key` |
| bootstrap 有效权益 `Bootstrap` | `user_id`，`revoked_at IS NULL`，`starts_at <= now`，`ends_at > now` 或为空 | `entitlement_grants_active_window_idx (user_id, ends_at, starts_at) WHERE revoked_at IS NULL` |
| 注销撤销权益 `DeleteAccount` | `user_id`，`revoked_at IS NULL`，`ends_at > now` 或为空 | 同上 |
| 赠送月链到期时间（bootstrap、兑换） | `redemptions.user_id`，`grant_ends_at > now`，再按 `entitlement_grant_id` 关联权益 | `redemptions_user_grant_end_idx (user_id, grant_ends_at DESC)`，加上 `entitlement_grants_pkey` |
| 同链最新到期 | `chain_id` | `redemptions_chain_idx (chain_id, month_ordinal)` |
| 幂等重放 | `user_id`，`idempotency_key` | `redemptions_user_id_idempotency_key_key` |
| 活动近似总量 | `campaign_key` 计数（事务外，允许少量超发） | `redemptions_campaign_user_idx (campaign_key, user_id)` |
| 账号兑换次数 | `(campaign_key, user_id)` | `redemption_account_usage_pkey` |
| 审计按用户、按来源查询（运维） | `user_id` 按时间；`(source_type, source_ref)` | `entitlement_audit_logs_user_time_idx`、`entitlement_audit_logs_source_idx`（部分索引，`source_ref IS NOT NULL`） |

### 5.5 迁移

| 迁移 | 内容 |
|---|---|
| `000001_create_store_metadata` | `store_metadata` |
| `000002_create_store_domain` | `pgcrypto` 扩展，以及 §5.3 的五张业务表、约束和 §5.4 的索引 |

- 上线前（2026-11-03）迁移整理过一次：开发阶段的 `000003`～`000006` 已合并进 `000002` 或直接删除，包括早期 IAP 表及其删除、索引调整、注销字段、审计事件类型收窄、删除未使用的 `entitlement_grants_lookup_idx`。所以生产首次部署执行的迁移里，没有"先建再删"的对象。
- 生产库从未执行过迁移，首次部署从版本 0 执行到 2。测试库执行过旧版本，需要一次性重建，步骤见[部署文档](deployment.md) §5.6「上线前整理迁移的一次性例外」。
- 部署时 Actions 先把迁移上传到 `migrations.next`，再整体替换服务器上的 `migrations` 目录。分支里删掉的迁移文件不会残留在服务器上被误执行。
- 已在全新 PostgreSQL 17 上验证：up 后结构与整理前 `000001`～`000006` 执行后的结构相比，只差上面两项计划内清理；down 到 0 后再 up，结构完全一致；`go test ./...`（含集成测试）通过。
- 从现在起 `000001`、`000002` 都视为已发布迁移，不能再改；表结构变更只能新增可回退的下一个版本。

## 6. 配置与部署

- 已删除的配置：`HUAWEI_IAP_*`、`PURCHASE_BINDING_SECRET`、`PURCHASE_TOKEN_ENCRYPTION_KEY`、`IAP_RECONCILIATION_*`、`IAP_BACKFILL_*`。服务器上已有的 `secrets/test.env`、`secrets/prod.env`、`.env.prod` 中的这些键可以删除，新代码不读取。
- 已删除的程序：`cmd/reconciler`、`internal/store/reconciliation`、`internal/huawei/iap`、`internal/huawei/jws`；`Dockerfile` 和 `deploy/docker-compose.prod.yml` 不再构建、启动 `iap-reconciler`。服务器上残留的 `onebeat-iap-reconciler` 容器需要手动删除。
- 需要在 AGC 手动处理：关闭 IAP 服务端通知地址（关键事件通知），否则华为会继续把订单通知推送到香港服务器（接口已返回 404）。

## 7. 测试

后端：

- `go vet ./...`、`go test ./...`。
- 集成测试设置 `ONEBEAT_INTEGRATION_DATABASE_URL` 后运行，需要使用全新数据库：`TestDeleteAccountDeidentifiesAndKeepsRedemptions`。
- 接口：`internal/httpapi/account_deletion_test.go`，包括已注销账号的会话被拒绝、重试幂等和旧购买接口返回 404。

客户端（`scripts/tests/run-sync-regressions.cjs` 门禁）：

- `store-iap-local.cjs`：
  - 用 Node 生成的 P-256 密钥签出真实 ES256 JWS。验签通过；篡改载荷、换密钥、非 ES256、无 x5c、自建证书链、根证书为空、正式包收到沙盒数据、未知环境时都失败；
  - 300 组随机签名的 DER 转换；订阅状态映射；
  - 先写盘后 `finishPurchase`、写盘失败不确认、确认失败下次重试、查询失败保留摘要、退款后消失、伪造数据只计入拒绝数、前台刷新节流；
  - 新购买顺序、重启恢复、正式包丢弃沙盒条目；
  - 合并优先级、华为月卡优先、两条互斥规则、超过 30 天快照有效期后付费权益仍有效。
- `store-user-feedback.cjs`：购买和恢复不登录、不访问服务器；兑换仍需登录，华为月卡有效时被拦截；注销弹窗文案。
- 共用的测试工具：`scripts/tests/iap-local-mocks.cjs`。

真机（沙盒，调试包）：

1. 不登录小铺，直接购买一个角色，能立即使用；重启后仍拥有；卸载重装后点「恢复购买」能找回。
2. 订阅月卡后状态正确；取消续费后显示「已取消续费，可使用至 …」。
3. 兑换仍需同意和登录；华为月卡有效时兑换被拦截。
4. 注销后已购商品仍在，兑换获得的月卡失效。
5. 抓包或查服务端日志，确认没有购买相关请求发到服务器。
6. 从日志取出 x5c 根证书并写入代码（§3.2 待办）。
