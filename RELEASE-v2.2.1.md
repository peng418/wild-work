# wild-work v2.2.1 — 积分显示修正：可用/不可用拆分 + 有效期 + 明细翻页 + 401 自愈

> 本版聚焦「积分到底有多少能用」这一件事，修正 TraeWork 可用余额虚高与面板数字误导，
> 解决「本地 token 看似有效但上游已拒绝」导致的积分恒 0，
> 并补齐面板的映射编辑与浮窗翻页体验。

## 修复

### API 配置对话框「映射表-编辑」无效 + 保存链路损坏（重要）

**现象**：点「编辑」无任何反应；就算改了映射/渠道/密钥，点保存也永远失败。

**根因**（三层叠加）：

1. `btnCompatMap` 从未绑定 onclick；
2. `serveLocked` 先 `net.Listen` 新地址再关闭旧 listener——同端口保存时撞自身
   （`Only one usage of each socket address`）→ `listen` 恒 400，
   **映射/渠道/密钥修改从未真正保存成功过**；
3. 映射编辑用的是 `prompt()`，语法错误只能弹窗循环。

**修复**：

- 同地址直接复用现有 listener；切地址先关闭旧服务再绑定（`App.listenAddr` 跟踪）；
- 对话框内嵌映射编辑器，就地校验（缺 `=`、目标非 `channel/model`、未知渠道、重复键），
  错误红字显示在编辑器下方，不再弹窗循环；
- **缺省建议按钮**：按已接入渠道给出可点选的映射起点（如
  `Claude Code → workbuddy: claude-* = workbuddy/glm-5.2`），
  一键插入并去重；未绑定渠道不显示建议，避免误导；
- **compat 热更新**：原实现路由表只在启动时加载一次，运行中改映射要重启才生效
  （这正是「有的机器没配映射也能用」的原因——config.json 启动时已带映射）。
  现面板保存后经 `gateway.SetCompat` 立即生效；
- 客户端请求路由到无账号渠道时，错误信息区分「未绑定账号」与「全部冷却/禁用」，
  引导用户去面板添加账号或换已接入渠道。

**映射写法**（已验证 CC/Codex 可正常使用）：

```
claude-* = workbuddy/glm-5.2          # CC 主模型
claude-sonnet-* = workbuddy/kimi-k2.7 # 更长前缀优先，盖过 claude-*
codex-* = traework/DeepSeek-V4-Pro    # Codex
gpt-5* = traework/glm-5.2            # gpt-5 / gpt-5.1 / gpt-5-codex 均命中
```

注意：未命中映射的裸名走 `default_channel` + 原始模型名（如 `workbuddy/gpt-4.1`），
上游多半无此模型会报错，需自行补映射。

### 本地 token 看似有效、上游已拒绝 → 永久卡在 401（重要）

**现象**：账号积分恒为 0，hover 不弹明细（接口 400）。

**根因**：刷新时上游会**作废旧 access token**（refresh token 同时轮换）。若新 token 未落盘，
或同一账号在另一实例/客户端上被刷新过，本地文件里就是**已被作废、但 `expiresAt` 仍在未来**的 token：

```
本地 expiresAt = 未来某时（看起来还有很多天）
NeedsRefresh(10min) → false  ← 永远不会去刷新
上游实际返回     → 401      ← 但 token 早已被作废
```

于是 `NeedsRefresh` 永远为假、永不重试，积分/明细永久为空。

**修复**：不再只信任本地过期时间，对 401 本身做一次「refresh + 重试」，成功后写回磁盘。
覆盖五条路径：积分自动刷新、单账号刷新（`RefreshCredits`）、批量刷新（`RefreshAll`）、
明细查询（`ResourceDetail`）、费率拉取（`RefreshPricing`，
原先连 refresh 结果都没落盘，已一并补上）。

**验证**（把 `accessToken` 改坏、保留有效 `refreshToken` 后启动）：

```
session dead, refreshing platform=traework uid=1096660468371514
traework refresh success uid=1096660468371514 refresh_rotated=true expires_at=1790917166
→ 积分 2710/不可用 2600，明细 29 条正常，新 token 已写回磁盘
```

### TraeWork 可消耗余额虚高（重要）

早期实现用 `group_type != 1` 判定可消耗额度，把 `available_endpoint=1`
（官方客户端专用池）的「用户福利」「签到奖励」也计入了可消耗余额。

**实测证据**（2026-09-18，三账号各做一次 `glm-5.2` 对话后对比用量）：

```
账号 3066985700146732（对话前 → 对话后）
  gt=1 ep=0 每日签到   used 420.7852 → 422.1424  (+1.3568)  ← 本工具只扣这里
  gt=1 ep=1 每日签到   used   0.0000 →   0.0000  (不动)
  gt=4 ep=1 用户福利   used   0.0000 →   0.0000  (不动)
```

判据已修正为 **`available_endpoint == 0`**。影响：

- `pool.Pick()` 不再按虚高余额选号（原先可能选中一个实际可用余额已耗尽的账号）
- 签到后解冻判定（`ReenableIfCredits`）同样只看可用池，不再被专用池额度误放行

### 总分与分项「对不上」的澄清

不是计算错误。分项合计与 `usage_summary` 严格自洽：

| 账号 | Σlimit | total_amount | Σused | consumed_amount |
|------|--------|--------------|-------|-----------------|
| 1096660468371514 | 5350 | 5350 | 40.4864 | 40.49 |
| 1342951362670730 | 9750 | 9750 | 3675.0232 | 3675.02 |
| 3066985700146732 | 9150 | 9150 | 2920.7852 | 2920.79 |

`used` 的小数尾差来自上游 `consumed_amount` 只保留两位小数。真正的坑是
**`total_amount` 是含 ep=1 专用池的总量**，把它当可用余额就会得出「账号有 9150 积分」的错觉
（该账号实际可用仅 1828）。现已把两者分开显示。

### 到期时间

各渠道字段不同，统一解析为 `YYYY-MM-DD` 并按 UTC+8 墙钟处理：

| 渠道 | 字段 | 说明 |
|------|------|------|
| WorkBuddy / 国际版 | `CycleEndTime` | **上游从不下发 `PackageEndTime`**（旧判据恒 miss） |
| TraeWork | `expire_time` | Unix 秒 |
| Qoder | 无 | 不显示有效期列 |

上游未下发时该列整体隐藏，不会出现一列空白或用零值冒充「永不过期」。

## 改进

### 面板：积分数字拆成「可用 / 不可用」

- 账号卡片：`1828可用积分/4400不可用`（不可用额度降级为次要色），主界面直接可见，无需 hover
- 明细 tooltip 底部：`可用 1828　不可用 4400`，与卡片口径一致
- 明细表中不可用额度整行淡显 + 「不可用」角标

### 面板：明细分页与有效期列

- 每页 8 条，翻页器在浮窗**顶部居中**（`‹ 1 / 4 ›`），标题/条数分列两端；
  鼠标从卡片到达按钮路径最短，不跨出浮窗边界
- 浮窗宽度固定 380px，翻页时不再变形/宽度跳动；单元格超长省略号
- 翻页只重绘不重新请求（响应已缓存）
- 新增「有效期」列（仅当上游确实下发到期时间时出现）
- 明细缓存随 `loadState()` 失效，避免刷新后 tooltip 仍显示旧余额
- 限额为 0 的包（如 TraeWork 的免费 0 限额包）不再出现在明细里

### 积分自动刷新覆盖全部渠道

原来自动刷新只覆盖 WorkBuddy 国际版，其余渠道积分只随签到更新——
签到间隔过长或查询失败时，面板数字长期不更新。现四渠道统一纳入循环：

- 启动立即首刷一次，之后每 30 分钟；
- qoder / workbuddyai 无签到活动，不自动刷就会一直显示旧值或 0；
- traework / workbuddy(CN) 签到间隔过长，统一纳入才能及时自愈 401。

### 旧版 state 兼容：无需删除 data 目录

v2.2.0 的 `state-*.json` 无 `unusable` 字段，直接换二进制会让卡片只显示
「xxx可用积分」而无不可用拆分（旧数字残留）。现已：

- state 文件增加 `version` 字段（v2 = 可用/不可用拆分口径）；旧格式读入后标记余额口径不可信；
- 卡片对这类账号显示「待刷新」（带说明），不把旧值当真值；
- 配合全渠道首刷，启动几秒内自动变为真实拆分数字。

**升级时保留 `data/` 目录即可**，无需删除或手工迁移。

## 内部改动

- `provider.ResourceItem` 增加 `ExpireAt` / `Usable`；新增 `provider.Summarize()` 统一汇总小计
- `pool.Status` / `AccountView` 增加 `UnusableCredits` / `CreditsStale`，随 `state-*.json` 持久化
- `pool.ReenableIfCredits(uid, remain, unusable)` 签名变更；`SetCredits` 并入 `SetCreditDetail`
- 积分刷新路径改走 `UserResourceDetail` 单次请求，同时拿到可用余额与小计（原先只调 `UserResource`）
- 三渠道 `UserResourceDetail` 增加 `Usable: true`（国内版/国际版/Qoder 无端点分区）
- gateway 增加 `SetCompat`（路由表热更新，带 RWMutex）与 `App.SetCompatSyncer` 注入点
- CI：tag 发版的 release note 改用仓库内 `RELEASE-<tag>.md`（缺失时回退自动生成）

## 升级说明

- **保留 `data/` 目录直接替换二进制即可**：旧 `state-*.json` 缺 `unusable` 字段时,
  面板会先显示「待刷新」，启动后自动刷新即变为真实拆分数字，无需手工迁移或删除目录；
- 若某账号的 refresh token 本身已失效（日志出现 `refresh token is invalid`），
  该账号需重新登录——这是唯一无法自愈的情况。
