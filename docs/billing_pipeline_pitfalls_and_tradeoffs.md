# 计费系统数据链路坑点与取舍文档

## 1. 文档目的

本文档聚焦计费系统从 MQ 消息进入，到表达式引擎计算，再到费用项落库的完整数据链路。

目标是梳理这条链路中的主要风险点，并给出不同设计方案的取舍建议，帮助团队在“灵活配置、开发成本、金融正确性、性能、可审计性”之间做平衡。

核心链路如下：

```text
MQ 原始字符串
  ↓
反序列化成 Event Struct
  ↓
根据 Fee Rule 解析依赖因子
  ↓
生成 CalculationContext
  ↓
转换成 map[string]interface{}
  ↓
传给 Govaluate
  ↓
得到计算结果
  ↓
金额落库 + 快照
```

这条链路可以做，但需要明确一个核心原则：

```text
map[string]interface{} 只能作为表达式引擎适配层，不能成为计费系统内部主数据模型。
```

---

## 2. 总体风险概览

| 阶段 | 主要坑点 | 严重程度 | 推荐策略 |
|---|---|---:|---|
| MQ 字符串 | 消息格式变化、字段缺失、重复投递 | 高 | schema_version + event_id + 原始消息保存 |
| 反序列化 | 数字变 float64、null 与 0 混淆 | 高 | 强类型 Struct + Decimal/string 金额 |
| Event Struct | 与业务结构强绑定，字段演进困难 | 中 | 转成 NormalizedEvent |
| 因子解析 | 因子依赖循环、取数失败、默认值滥用 | 高 | DAG + typed factor + 显式失败策略 |
| CalculationContext | 类型语义丢失、来源不可追踪 | 高 | Typed Factor Context |
| map[string]interface{} | 类型降级、运行时错误 | 高 | 最后一公里转换，转换前强校验 |
| Govaluate | 金额精度、float64、变量名、时间比较 | 高 | Decimal 适配 / minor unit / 限制表达式能力 |
| 结果落库 | 幂等、覆盖历史、审计缺失 | 高 | append-only + idempotency key + snapshot |
| 历史重算 | 当前规则污染历史结果 | 高 | rule version + factor snapshot + as-of-time |

---

## 3. 坑点一：MQ 原始字符串不是可信输入

### 3.1 问题描述

入口从 MQ 拿到的是字符串，存在以下风险：

```text
1. 消息格式不符合预期；
2. 上游新增、删除、修改字段；
3. 字段类型变化，例如 amount 从 string 变成 number；
4. 消息重复投递；
5. 消息乱序；
6. 同一个业务事件被多个 topic 投递；
7. 事件缺少全局唯一 ID；
8. 消息 schema 没有版本。
```

如果直接进入计费计算，会导致后续错误难以定位。

---

### 3.2 可选方案

#### 方案 A：直接反序列化当前结构体

```text
MQ String -> PaymentEvent Struct
```

优点：

```text
实现简单，开发成本低。
```

缺点：

```text
对上游字段变化敏感；
缺少 schema 演进能力；
不利于多事件类型统一处理。
```

适用场景：

```text
早期 PoC，事件类型少，上游稳定。
```

---

#### 方案 B：引入 schema_version + event envelope

消息统一包装：

```json
{
  "event_id": "evt_001",
  "event_type": "PAYMENT_SUCCESS",
  "schema_version": "v1",
  "biz_time": "2026-05-15T10:30:00Z",
  "payload": {}
}
```

优点：

```text
有利于版本演进；
有利于多事件统一处理；
有利于幂等和审计。
```

缺点：

```text
需要上游配合；
需要维护不同版本的 parser。
```

推荐程度：高。

---

### 3.3 推荐取舍

推荐至少要求上游提供：

```text
event_id
biz_scene / event_type
schema_version
biz_time
merchant_id
biz_order_id
```

计费系统应保存原始消息或原始消息引用：

```text
raw_message / raw_message_hash / raw_message_storage_key
```

这样即使后续解析逻辑变化，也可以追溯原始输入。

---

## 4. 坑点二：JSON 数字反序列化后可能变成 float64

### 4.1 问题描述

如果直接将 MQ JSON 反序列化成：

```go
map[string]interface{}
```

JSON 中的数字通常会变成 `float64`。

例如：

```json
{
  "payment_amount": 100.23
}
```

可能得到：

```go
map["payment_amount"] = float64(100.23)
```

金融系统里这是高风险问题，因为金额不能依赖二进制浮点数。

---

### 4.2 可选方案

#### 方案 A：金额字段使用 string

```json
{
  "payment_amount": "100.23"
}
```

Go 结构体：

```go
type PaymentEvent struct {
    PaymentAmount string `json:"payment_amount"`
}
```

优点：

```text
避免 JSON number 进入 float64；
跨语言稳定；
适合 Decimal 解析。
```

缺点：

```text
需要额外校验格式；
上游需要遵守约定。
```

推荐程度：高。

---

#### 方案 B：金额字段使用 minor unit int64

```json
{
  "payment_amount_minor": 10023,
  "currency": "USD"
}
```

含义：

```text
100.23 USD = 10023 cents
```

优点：

```text
计算精确；
数据库存储简单；
适合金融系统。
```

缺点：

```text
多币种 scale 需要额外处理；
表达式可读性较差；
百分比、分摊、rounding 仍需要谨慎。
```

推荐程度：高，尤其适合最终入账金额。

---

#### 方案 C：反序列化为 decimal.Decimal

```go
type PaymentEvent struct {
    PaymentAmount decimal.Decimal `json:"payment_amount"`
}
```

优点：

```text
业务表达自然；
避免 float64。
```

缺点：

```text
依赖具体 decimal 库；
JSON 输入格式仍需要约束；
表达式引擎是否支持 Decimal 需要额外适配。
```

推荐程度：中高。

---

### 4.3 推荐取舍

推荐：

```text
MQ 层金额使用 string 或 minor unit；
系统内部统一转成 Decimal 或 Money 类型；
落库金额使用 decimal 字段或 minor unit int64；
禁止金额以 float64 进入 CalculationContext。
```

---

## 5. 坑点三：字段缺失、null、0 容易混淆

### 5.1 问题描述

这三种情况含义不同：

```json
{}
```

```json
{"discount_amount": null}
```

```json
{"discount_amount": "0"}
```

分别代表：

```text
字段不存在
字段存在但为空
字段存在且值为 0
```

如果系统直接把缺失或 null 转成 0，会导致静默算错。

---

### 5.2 可选方案

#### 方案 A：缺失字段默认零值

优点：

```text
实现简单。
```

缺点：

```text
金融系统高风险；
错误不易发现；
无法区分真实 0 和缺失。
```

推荐程度：低。

---

#### 方案 B：FactorValue 显式保存状态

```go
type FactorStatus string

const (
    FactorOK      FactorStatus = "OK"
    FactorMissing FactorStatus = "MISSING"
    FactorNull    FactorStatus = "NULL"
    FactorInvalid FactorStatus = "INVALID"
)

type FactorValue struct {
    Code   string
    Status FactorStatus
    Value  any
    Err    error
}
```

优点：

```text
语义清晰；
便于错误处理；
便于审计。
```

缺点：

```text
实现稍复杂。
```

推荐程度：高。

---

### 5.3 推荐取舍

推荐原则：

```text
缺失就是缺失；
null 就是 null；
0 就是 0；
是否允许默认值，由因子配置明确声明。
```

示例：

```yaml
factor_code: discount_amount
required: false
default_value: "0"
```

没有配置 default_value 时，缺失字段不允许静默变成 0。

---

## 6. 坑点四：Event Struct 与表达式强绑定

### 6.1 问题描述

如果表达式直接引用消息结构：

```text
event.Payment.Amount * 0.2
```

会带来问题：

```text
1. 表达式和 Go Struct 强绑定；
2. 字段重构会影响历史规则；
3. 不同 schema_version 难以兼容；
4. 表达式暴露过多内部结构；
5. 审计时难以解释业务因子。
```

---

### 6.2 可选方案

#### 方案 A：表达式直接访问 Struct 字段

优点：

```text
少一层因子映射，开发简单。
```

缺点：

```text
规则和代码结构耦合；
历史重算风险高；
不适合配置平台。
```

推荐程度：低。

---

#### 方案 B：表达式只引用标准因子

表达式：

```text
base_amount * service_fee_rate
```

因子配置：

```yaml
factor_code: base_amount
factor_type: EVENT_FIELD
source_path: "$.payment.amount"
```

优点：

```text
表达式稳定；
消息结构变化只影响因子配置或 NormalizedEvent；
更适合审计和平台化。
```

缺点：

```text
需要维护因子定义。
```

推荐程度：高。

---

### 6.3 推荐取舍

推荐引入：

```text
NormalizedEvent + Factor Definition
```

不要让 Fee Rule 表达式直接依赖上游 MQ JSON 或 Go Struct。

---

## 7. 坑点五：NormalizedEvent 是否值得引入

### 7.1 问题描述

从 MQ 反序列化成 Event Struct 后，是否需要再转一层 NormalizedEvent？

---

### 7.2 可选方案

#### 方案 A：直接从 Event Struct 取因子

优点：

```text
链路短；
开发快。
```

缺点：

```text
多个消息版本、多事件类型时复杂度上升；
因子 source_path 可能绑定具体上游格式；
字段语义不统一。
```

适用：

```text
事件类型少，字段稳定，短期项目。
```

---

#### 方案 B：统一转成 NormalizedEvent

```text
PaymentEventV1 / PaymentEventV2 / RefundEventV1
  ↓
NormalizedBillingEvent
```

优点：

```text
隔离上游 schema 变化；
因子 source_path 稳定；
多个事件类型统一处理；
有利于审计和重放。
```

缺点：

```text
多一层转换成本；
需要设计标准事件模型。
```

推荐程度：中高。

---

### 7.3 推荐取舍

如果系统要长期平台化，建议引入 NormalizedEvent。

可以分阶段：

```text
Phase 1：强类型 Event Struct + 简单标准化字段；
Phase 2：完整 NormalizedBillingEvent；
Phase 3：多 schema_version parser + 回放兼容。
```

---

## 8. 坑点六：因子依赖可能形成复杂 DAG

### 8.1 问题描述

因子之间可能互相依赖：

```text
Fee103 depends on service_fee_rate
service_fee_rate depends on merchant_level, country, payment_success_time
merchant_level depends on RPC(merchant_id)
merchant_id depends on EVENT_FIELD
```

如果不建模依赖关系，会出现：

```text
1. 解析顺序错误；
2. 重复调用 RPC；
3. 循环依赖；
4. 某个因子失败导致错误扩散；
5. 并发解析不可控。
```

---

### 8.2 可选方案

#### 方案 A：按表达式逐个费用项递归解析

优点：

```text
实现简单。
```

缺点：

```text
重复解析因子；
循环依赖难发现；
性能差；
难以统一错误处理。
```

推荐程度：低。

---

#### 方案 B：构建因子依赖 DAG

```text
1. 收集所有 Fee Rule 依赖因子；
2. 根据因子配置展开依赖；
3. 构建 DAG；
4. 检查循环依赖；
5. 按拓扑顺序解析；
6. 相同因子同一事件内只解析一次。
```

优点：

```text
依赖清晰；
避免重复解析；
可并行；
更适合审计和调试。
```

缺点：

```text
实现成本较高。
```

推荐程度：高。

---

### 8.3 推荐取舍

即使 Phase 1 不实现复杂并行，也建议从一开始就有 DAG 概念。

最小实现：

```text
1. 发布前检查循环依赖；
2. 运行时拓扑排序；
3. 同一事件内因子缓存。
```

---

## 9. 坑点七：RPC 因子不稳定

### 9.1 问题描述

RPC 因子会引入外部不确定性：

```text
1. RPC 超时；
2. RPC 返回值变化；
3. 接口字段变更；
4. 下游服务不可用；
5. 历史重算时拿到当前值；
6. 单事件多费用项导致 RPC 风暴。
```

---

### 9.2 可选方案

#### 方案 A：计算时实时 RPC

优点：

```text
数据实时；
不需要本地同步。
```

缺点：

```text
稳定性受外部影响；
性能不可控；
历史重算可能不一致。
```

适用：

```text
低频、非核心金额字段、可容忍失败的因子。
```

---

#### 方案 B：事件消息携带业务事实快照

优点：

```text
计算稳定；
重算一致；
性能好。
```

缺点：

```text
上游消息变大；
需要上游保证快照正确。
```

适用：

```text
订单金额、支付金额、退款金额、支付时间、币种等核心事实。
```

---

#### 方案 C：本地快照表 / 版本表

优点：

```text
支持 as-of-time 查询；
稳定性好；
适合历史重算。
```

缺点：

```text
需要数据同步和一致性治理。
```

适用：

```text
商家等级、类目、协议、配置类业务事实。
```

---

### 9.3 推荐取舍

建议优先级：

```text
核心交易事实：优先从事件快照取；
历史敏感属性：优先本地版本表或 as-of-time RPC；
低频辅助属性：可以 RPC，但必须有 timeout、失败策略和快照。
```

RPC 因子必须注册 provider，不允许业务方随便填接口。

---

## 10. 坑点八：RULE_TABLE 匹配可能冲突或无命中

### 10.1 问题描述

费率因子通常来自 RULE_TABLE：

```text
service_fee_rate = 根据 country、merchant_level、payment_method、payment_success_time 匹配规则表
```

风险：

```text
1. 无规则命中；
2. 多条规则命中；
3. 时间区间重叠；
4. 通配符规则覆盖了更精确规则；
5. priority 配置错误；
6. 历史规则被修改导致重算结果变化。
```

---

### 10.2 可选方案

#### 方案 A：运行时命中多条取第一条

优点：

```text
简单。
```

缺点：

```text
不可解释；
容易静默算错。
```

推荐程度：低。

---

#### 方案 B：priority + 冲突检测

```text
1. 多条命中时按 priority 降序；
2. 同一最高 priority 命中多条则报错；
3. 发布前检查同优先级时间区间重叠。
```

优点：

```text
可解释；
可治理；
适合平台配置。
```

缺点：

```text
配置平台复杂度增加。
```

推荐程度：高。

---

### 10.3 推荐取舍

RULE_TABLE 必须支持：

```text
1. no_match_strategy；
2. conflict_strategy；
3. priority；
4. effective_from / effective_to；
5. 发布前冲突检测；
6. 命中行快照。
```

无命中时不建议默认 0 费率。

---

## 11. 坑点九：表达式引擎 Govaluate 的金额精度问题

### 11.1 问题描述

Govaluate 的输入通常是：

```go
map[string]interface{}
```

数字运算可能进入 `float64`。

对金融金额来说，风险很高。

例如：

```text
0.1 + 0.2 != 0.3
```

这类误差不能出现在结算系统中。

---

### 11.2 可选方案

#### 方案 A：直接用 Govaluate 原生算术

表达式：

```text
base_amount * service_fee_rate
```

参数：

```go
base_amount = 100.23
service_fee_rate = 0.2
```

优点：

```text
表达式自然；
实现简单。
```

缺点：

```text
金额精度风险高；
不适合金融结算。
```

推荐程度：低。

---

#### 方案 B：使用 minor unit int64

表达式：

```text
base_amount_cent * rate_ppm / 1000000
```

优点：

```text
计算精确；
性能好。
```

缺点：

```text
表达式不直观；
rounding、比例计算仍需规范；
运营配置体验差。
```

推荐程度：中。

---

#### 方案 C：Govaluate 只做函数调用，函数内部使用 Decimal

表达式：

```text
mul(base_amount, service_fee_rate)
```

或：

```text
round(mul(base_amount, service_fee_rate), 2)
```

函数：

```go
mul(a, b) -> decimal.Decimal
```

优点：

```text
可以控制精度；
保留一定表达式能力。
```

缺点：

```text
表达式不如自然算术直观；
需要严格限制函数和参数类型；
Govaluate 对自定义类型兼容性需要验证。
```

推荐程度：中高，需要 PoC。

---

#### 方案 D：封装 Billing DSL 或更换表达式引擎

业务仍写：

```text
base_amount * service_fee_rate
```

但内部由 Billing DSL 解析并使用 Decimal 执行。

优点：

```text
业务表达自然；
金融精度可控；
长期稳定。
```

缺点：

```text
研发成本高；
需要自研或深度封装。
```

推荐程度：长期推荐。

---

### 11.3 推荐取舍

短期建议：

```text
对 Govaluate 做严格封装；
禁止金额直接以 float64 传入；
优先验证 Decimal 自定义函数方案；
表达式结果必须经过 Decimal 归一化和 rounding。
```

长期建议：

```text
抽象 BillingExpressionEngine 接口，避免直接绑定 Govaluate；
必要时替换为支持 Decimal 更好的表达式引擎或自研 DSL。
```

---

## 12. 坑点十：map[string]interface{} 会丢失类型安全

### 12.1 问题描述

`map[string]interface{}` 的问题：

```text
1. 编译期无类型检查；
2. 因子值可能是 string、float64、decimal、int 混用；
3. 错误延迟到运行时；
4. 不容易区分业务类型；
5. 不利于审计和快照。
```

---

### 12.2 可选方案

#### 方案 A：内部全程使用 map[string]interface{}

优点：

```text
开发简单；
灵活。
```

缺点：

```text
类型风险最高；
问题排查困难；
不适合金融系统。
```

推荐程度：低。

---

#### 方案 B：内部使用 Typed Factor Context，最后转换成 map

```go
type FactorValue struct {
    Code       string
    DataType   FactorDataType
    Value      any
    RawValue   any
    SourceType string
    SourceMeta map[string]any
    Status     FactorStatus
}

type CalculationContext struct {
    EventID string
    Factors map[string]FactorValue
}
```

转换：

```text
Typed Factor Context -> Govaluate Params
```

优点：

```text
类型安全更强；
错误更早暴露；
便于审计；
便于快照。
```

缺点：

```text
实现成本更高。
```

推荐程度：高。

---

### 12.3 推荐取舍

推荐：

```text
map[string]interface{} 只在 ExpressionEngineAdapter 内部出现。
```

对外接口应是：

```go
Evaluate(expression CompiledExpression, ctx CalculationContext) (MoneyResult, error)
```

而不是：

```go
Evaluate(expression string, params map[string]interface{}) (interface{}, error)
```

---

## 13. 坑点十一：时间语义容易错

### 13.1 问题描述

计费系统里至少有多个时间：

```text
payment_success_time
refund_success_time
order_create_time
event_time
mq_publish_time
mq_consume_time
rule_publish_time
rule_effective_time
```

如果没有明确语义，会导致费率匹配、历史重算错误。

---

### 13.2 典型错误

```text
1. 用 MQ 消费时间匹配费率；
2. 用当前系统时间重算历史订单；
3. 退款冲正时用退款时间重新匹配原支付费率；
4. 表达式中直接比较时间字符串；
5. 时区不统一。
```

---

### 13.3 推荐取舍

明确每类场景的业务时间：

```text
支付费用：payment_success_time
退款冲正：原支付费用项快照时间或原支付快照
退款手续费：refund_success_time
规则发布时间：published_at
规则业务生效时间：effective_from / effective_to
```

表达式中不推荐直接做时间判断。

时间匹配应由 RULE_TABLE Resolver 处理。

---

## 14. 坑点十二：表达式变量名不规范

### 14.1 问题描述

如果变量名允许：

```text
payment-amount
payment.amount
103Fee
服务费率
```

可能导致表达式解析歧义或语法问题。

例如：

```text
payment-amount
```

可能被解析成：

```text
payment - amount
```

---

### 14.2 推荐取舍

因子编码统一限制为：

```text
[a-zA-Z_][a-zA-Z0-9_]*
```

推荐风格：

```text
base_amount
service_fee_rate
payment_success_time
merchant_level
```

不建议在表达式中使用原始 JSON path。

---

## 15. 坑点十三：表达式能力过强会失控

### 15.1 问题描述

如果允许表达式写复杂逻辑：

```text
if country == 'US' && payment_time < '2026-06-01' then ...
```

会导致：

```text
1. 规则逻辑散落在表达式里；
2. 规则表失去意义；
3. 审计困难；
4. 配置人员需要理解脚本；
5. 冲突检测无法提前完成。
```

---

### 15.2 可选方案

#### 方案 A：表达式支持完整条件判断

优点：

```text
灵活。
```

缺点：

```text
治理困难；
配置风险高。
```

推荐程度：低。

---

#### 方案 B：表达式只支持金额公式，条件规则放在 Fee Rule / RULE_TABLE

优点：

```text
边界清晰；
适合平台化；
可做发布前校验。
```

缺点：

```text
某些复杂公式需要拆成多个因子或规则表。
```

推荐程度：高。

---

### 15.3 推荐取舍

建议表达式能力限制为：

```text
加减乘除
括号
min/max/abs
round
coalesce，可选且需谨慎
```

不建议在表达式中承载复杂业务分支。

---

## 16. 坑点十四：表达式解析性能与缓存

### 16.1 问题描述

如果每条 MQ 消息都重新 parse 表达式，会造成不必要的性能开销。

---

### 16.2 推荐取舍

表达式应在规则发布或服务加载时预编译：

```text
cache_key = fee_code + rule_version + expression_hash
```

运行时只执行 Evaluate。

需要注意：

```text
1. 规则发布后刷新缓存；
2. 灰度版本缓存隔离；
3. 回滚后缓存失效；
4. 表达式函数版本要可控。
```

---

## 17. 坑点十五：落库只存结果，不存过程

### 17.1 问题描述

如果只存：

```text
fee_code = Fee103
amount = 20.00
```

后续无法解释：

```text
为什么是 20？
费率是多少？
用的是哪条规则？
当时 base_amount 是多少？
```

---

### 17.2 推荐取舍

每个费用项必须保存：

```text
1. rule_id；
2. rule_version；
3. expression；
4. factor_snapshot；
5. rule_table matched_row；
6. rounding_config；
7. final_amount；
8. idempotency_key。
```

可以考虑冷热分离：

```text
fee_item 表保存核心字段；
factor_snapshot 表保存大 JSON；
历史快照可以归档到低成本存储。
```

---

## 18. 坑点十六：历史重算不一致

### 18.1 问题描述

历史订单重算时，如果使用当前规则和当前 RPC 返回值，结果可能和原计算不同。

---

### 18.2 可选方案

#### 方案 A：完全按当前规则重算

适用：

```text
模拟新规则影响。
```

不适用：

```text
审计解释原费用。
```

---

#### 方案 B：按原规则版本 + 原因子快照重放

适用：

```text
审计、解释、复现历史结果。
```

---

#### 方案 C：按原规则版本 + as-of-time 重新取因子

适用：

```text
修复因子快照缺失，或验证历史数据。
```

前提：

```text
外部因子支持 as-of-time 查询。
```

---

### 18.3 推荐取舍

重算需要区分目的：

```text
解释历史结果：使用原规则版本 + 原因子快照；
修复错误：使用指定规则版本 + 指定因子版本；
模拟新规则：使用新规则 + 历史事件样本。
```

不能只有一种“重算”。

---

## 19. 坑点十七：幂等键设计不清晰

### 19.1 问题描述

MQ 重复投递时，可能重复落费用项。

---

### 19.2 推荐取舍

事件幂等：

```text
event_id 唯一
```

费用项幂等：

```text
event_id + fee_code + fee_direction + rule_version
```

或者根据业务语义：

```text
biz_order_id + event_type + fee_code + fee_direction
```

需要确认上游事件是否满足：

```text
同一业务动作只有一个 event_id；
重试消息 event_id 不变；
补发消息是否使用新 event_id。
```

---

## 20. 坑点十八：退款场景容易算错

### 20.1 问题描述

退款场景有两类完全不同语义：

```text
1. 冲正原支付费用；
2. 产生新的退款手续费。
```

如果混在一起，会导致费率时间选择错误。

---

### 20.2 推荐取舍

对于冲正原费用：

```text
优先引用原 fee_item 的 factor_snapshot；
不要用退款时间重新匹配支付费率。
```

对于退款手续费：

```text
使用 refund_success_time 匹配 refund_fee_rate。
```

平台上需要明确费用项方向：

```text
CHARGE
REFUND_REVERSAL
REFUND_FEE
ADJUSTMENT
```

---

## 21. 坑点十九：规则表查询性能与索引

### 21.1 问题描述

RULE_TABLE 可能包含：

```text
国家
商家等级
支付方式
类目
商家 ID
时间区间
通配符
priority
```

如果直接查 MySQL，可能出现性能问题。

---

### 21.2 可选方案

#### 方案 A：每次实时查 MySQL

优点：

```text
简单；
配置变更立即生效。
```

缺点：

```text
高 QPS 下压力大；
规则表复杂时索引难设计。
```

适用：

```text
低 QPS 或早期阶段。
```

---

#### 方案 B：规则表加载到本地内存

优点：

```text
性能好；
避免频繁 DB 查询。
```

缺点：

```text
需要版本发布和缓存刷新机制；
多实例一致性需要处理。
```

适用：

```text
高 QPS、规则量可控。
```

---

#### 方案 C：MySQL + 本地短缓存

优点：

```text
折中方案；
实现成本适中。
```

缺点：

```text
仍需处理缓存失效和版本。
```

---

### 21.3 推荐取舍

Phase 1 可以 MySQL 查询 + 单事件内缓存。

后续建议：

```text
规则发布后生成不可变版本；
服务加载 active rule table version 到内存；
通过版本号切换生效；
保留 MySQL 作为配置源和审计源。
```

---

## 22. 坑点二十：配置自由度和治理成本的平衡

### 22.1 问题描述

计费平台希望灵活，但越灵活越危险。

自由度过高会导致：

```text
1. 配置人员写复杂逻辑；
2. 规则无法验证；
3. 表达式不可审计；
4. 线上问题难排查；
5. 平台变成低代码脚本系统。
```

自由度过低会导致：

```text
1. 很多需求仍要研发开发；
2. 配置平台价值下降；
3. 规则变化响应慢。
```

---

### 22.2 推荐取舍

建议采用“有限灵活”的方式：

```text
1. 表达式只表达金额公式；
2. 条件匹配放在 Fee Rule 和 RULE_TABLE；
3. 取数逻辑通过 Factor Type 标准化；
4. 外部访问通过 Provider Registry 管理；
5. SQL 不开放给业务方；
6. if/else 不开放给业务方；
7. 所有配置发布前必须模拟试算和审批。
```

---

## 23. 推荐的最终链路

推荐最终链路如下：

```text
MQ Raw String
  ↓
Envelope Parse
  ↓
Schema Validation
  ↓
Strong Typed Event Parser
  ↓
NormalizedBillingEvent
  ↓
Idempotency Check
  ↓
Fee Rule Match
  ↓
Factor Dependency DAG Build
  ↓
Factor Resolve
    ├── EVENT_FIELD
    ├── RPC
    ├── TABLE_LOOKUP
    ├── RULE_TABLE
    ├── EXPRESSION
    └── CONSTANT
  ↓
Typed CalculationContext
  ↓
ExpressionEngineAdapter
  ↓
Controlled map[string]interface{}
  ↓
Govaluate Evaluate
  ↓
Decimal Result Normalize
  ↓
Rounding
  ↓
Fee Item Append-only Insert
  ↓
Factor Snapshot / Rule Snapshot / Result Snapshot
```

---

## 24. 取舍建议汇总

| 问题 | 短期取舍 | 长期取舍 |
|---|---|---|
| MQ 输入 | 强类型 Struct + event_id | Event Envelope + schema_version |
| 事件模型 | 直接标准化关键字段 | 完整 NormalizedBillingEvent |
| 金额类型 | string -> Decimal | Money 类型 / minor unit + Decimal 计算 |
| 因子上下文 | Typed Factor Context | 完整 factor lineage + snapshot |
| 表达式引擎 | Govaluate 封装 | Billing DSL / 可替换表达式引擎 |
| 费率匹配 | RULE_TABLE + MySQL | 规则版本内存加载 + 冲突检测 |
| RPC 因子 | 限制使用 + timeout | Provider Registry + as-of-time |
| 重算 | 原因子快照复现 | 多模式 replay：解释/修复/模拟 |
| 配置平台 | 表格配置 + 试算 | 审批、灰度、回滚、diff |
| 审计 | 保存 factor_snapshot | 完整计算解释链路 |

---

## 25. 最小可落地版本建议

如果需要先快速落地，建议最小版本必须包含：

```text
1. MQ 消息强类型解析；
2. event_id 幂等；
3. 金额字段禁止 float64；
4. Fee Rule 表达式；
5. EVENT_FIELD 因子；
6. RULE_TABLE 因子；
7. Typed Factor Context；
8. Govaluate Adapter；
9. 表达式预编译缓存；
10. fee_item append-only；
11. factor_snapshot；
12. 规则表无命中和多命中错误处理。
```

可以暂缓：

```text
1. 完整审批流；
2. RPC 因子复杂治理；
3. 规则表内存引擎；
4. 批量历史重算；
5. 高级灰度；
6. 完整可视化解释页面。
```

---

## 26. 最重要的结论

这条链路的最大坑不是 Govaluate 本身，而是：

```text
从强类型事件逐步降级到 map[string]interface{} 后，类型安全、金额精度、字段缺失、时间语义和审计信息都会变弱。
```

因此核心取舍是：

```text
允许 map[string]interface{} 存在，但只允许它存在于 ExpressionEngineAdapter 内部。
```

系统内部必须坚持：

```text
1. 强类型事件；
2. 标准化事件；
3. Typed Factor Context；
4. Decimal/Money 金额类型；
5. 因子状态显式表达；
6. 规则和因子快照完整保存。
```

一句话总结：

```text
这条链路可以做，但不能让表达式引擎的动态参数模型反向污染整个计费领域模型。Govaluate 只是最后的计算适配器，计费系统真正的核心应该是强类型事件、因子解析、规则表匹配、金额精度控制和可审计快照。
```

