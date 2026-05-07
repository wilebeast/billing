# TikTok 电商商家结算计费系统重构技术方案

## 1. 背景

当前计费系统服务于 TikTok 电商的商家结算场景。上游在用户完成支付、退款等交易动作后，通过 MQ 投递交易事件。计费系统消费消息后，需要按照不同国家、商家、支付方式、类目、活动、协议等规则，计算并落地对应费用项。

典型费用项示例：

```text
Fee103 = base_amount * service_fee_rate
```

其中：

```text
base_amount          计费基数，例如支付金额、退款金额、结算基数
service_fee_rate     服务费率，可能随支付时间、国家、商家等级、支付方式变化
```

过去如果每个费用项都通过代码硬编码实现，会出现如下问题：

```text
1. 规则变化频繁，研发响应成本高；
2. 不同国家、商家、支付方式规则差异大，代码分支膨胀；
3. 财务、运营、结算规则难以在线配置和灰度；
4. 历史费用无法稳定重算；
5. 费用计算过程缺少完整审计链路；
6. 金额计算、字段缺失、RPC 取数等异常难以统一治理。
```

因此需要将计费能力平台化，形成一套可配置、可审计、可回放、可重算的计费规则系统。

---

## 2. 建设目标

### 2.1 业务目标

```text
1. 支持支付、退款、调整等结算事件的费用项计算；
2. 支持不同业务场景、国家、商家、类目、支付方式下的差异化计费规则；
3. 支持费率、固定费用、封顶金额、保底金额等参数配置化；
4. 支持费用项表达式配置，例如 base_amount * service_fee_rate；
5. 支持规则版本、生效时间、灰度和回滚；
6. 支持历史订单重算和审计解释；
7. 支持重复消息幂等处理，避免重复计费；
8. 支持异常进入重试或人工处理，而不是静默算错。
```

### 2.2 技术目标

```text
1. 规则配置与计算执行解耦；
2. 表达式引擎只负责纯计算，不负责取数；
3. 因子系统负责统一取数、类型转换、依赖解析和快照记录；
4. 支持 EVENT_FIELD、RPC、RULE_TABLE、TABLE_LOOKUP、EXPRESSION、CONSTANT 等多种因子取值方式；
5. 支持因子依赖 DAG，避免循环依赖；
6. 支持 Decimal 或 minor unit 金额计算，避免 float64 精度问题；
7. 支持每次计算保存 rule version、expression、factor snapshot、result snapshot；
8. 支持规则发布前校验、模拟试算和冲突检测；
9. 支持可观测性，包括 trace、日志、指标、费用计算解释链路。
```

---

## 3. 非目标

本期不建议把计费平台设计成通用脚本平台。

不推荐支持：

```text
1. 业务方直接编写任意 if/else 脚本；
2. 业务方直接配置任意 SQL；
3. 在表达式中直接调用 RPC；
4. 在表达式中直接访问完整 Event Struct；
5. 在表达式中做复杂时间判断；
6. 让表达式承担规则匹配职责。
```

推荐方向是：

```text
配置化规则 = 受控 DSL + 受控因子取数 + 受控规则表匹配 + 强审计。
```

---

## 4. 核心设计原则

### 4.1 表达式引擎只做纯计算

表达式中只允许引用已经解析好的因子：

```text
Fee103 = base_amount * service_fee_rate
```

不允许在表达式中写：

```text
Fee103 = rpc("GetRate", merchant_id) * amount
Fee103 = if(payment_time < '2026-06-01', amount * 0.2, amount * 0.18)
```

原因：

```text
1. RPC、查库、时间匹配、默认值、异常处理应该由因子系统负责；
2. 表达式引擎只承担确定性计算；
3. 这样更容易审计、重算、缓存、限流和治理。
```

---

### 4.2 因子类型按“数据获取逻辑”区分

因子类型不建议按业务语义划分，例如 RATE、AMOUNT、MERCHANT_ATTR。

更推荐按取值方式划分：

```text
EVENT_FIELD      从事件消息中取值
RPC              调外部服务取值
TABLE_LOOKUP     精确查表取值，通常依赖唯一键
RULE_TABLE       规则表匹配取值，支持时间区间、通配符、优先级
EXPRESSION       由其他因子表达式派生
CONSTANT         固定常量
DB_QUERY         受控本地查询，必要时使用
```

业务语义可以作为辅助分类：

```text
factor_category = RATE / AMOUNT / TIME / MERCHANT_ATTRIBUTE / ORDER_ATTRIBUTE
```

但 `factor_category` 不参与执行分派。

---

### 4.3 费率也是因子，但不是特殊执行类型

例如：

```text
service_fee_rate
```

它在业务上是费率，但在系统执行上可以是：

```text
factor_type = RULE_TABLE
factor_category = RATE
```

意思是：

```text
service_fee_rate 的值来自规则表匹配结果。
```

---

### 4.4 RPC 取业务事实，规则系统决定计费参数

RPC 因子适合获取业务事实：

```text
商家等级
商家国家
类目
订单属性
风控标签
账户属性
```

但费率、封顶金额、保底金额、固定手续费等计费参数，原则上应该由计费规则系统或规则表管理，而不是由普通业务 RPC 实时返回。

---

### 4.5 金额计算必须可控

计费系统不应使用 `float64` 作为金额计算基础。

推荐：

```text
1. 使用 decimal.Decimal；或
2. 使用 minor unit int64，例如 100.23 USD = 10023 cents。
```

表达式引擎如果天然基于 `float64`，必须做适配或限制。

---

### 4.6 所有计算结果必须可解释

每个费用项落库时，必须保存：

```text
1. 费用项编码；
2. 表达式；
3. 规则版本；
4. 因子快照；
5. 规则表命中记录；
6. RPC 请求和响应摘要；
7. 金额结果；
8. 舍入规则；
9. 事件 ID 和幂等键。
```

目标是可以回答：

```text
这笔费用为什么是 20.00？
当时 base_amount 是多少？
service_fee_rate 是怎么匹配出来的？
命中了哪个规则表行？
使用的是哪个规则版本？
为什么不是另一个费率？
```

---

## 5. 总体架构

### 5.1 服务拆分建议

可以拆成以下模块或服务：

```text
1. Billing Ingestion Service
   负责 MQ 消费、消息反序列化、幂等校验、事件标准化。

2. Billing Calculation Service
   负责费用项匹配、因子解析、表达式计算、金额归一化。

3. Billing Rule Service
   负责费用项规则、因子定义、规则表、版本、生效时间、发布审批。

4. Factor Provider / Resolver Layer
   负责 EVENT_FIELD、RPC、TABLE_LOOKUP、RULE_TABLE、EXPRESSION 等因子解析。

5. Fee Posting Service
   负责费用项落库、幂等写入、append-only 账务记录。

6. Billing Audit & Replay Service
   负责计算解释、历史重算、差异对比、人工处理。

7. Billing Admin Platform
   负责规则配置、因子配置、规则表配置、模拟试算、审批发布、回滚。
```

物理上可以先做成一个服务内的模块化架构，后续按复杂度拆分为多个服务。

---

### 5.2 数据流

```text
MQ 原始消息字符串
  ↓
反序列化成强类型 Event Struct
  ↓
事件标准化 NormalizedEvent
  ↓
幂等校验
  ↓
匹配 Fee Rule
  ↓
收集表达式依赖因子
  ↓
构建因子依赖 DAG
  ↓
按拓扑顺序解析因子
  ↓
构建 Typed Calculation Context
  ↓
转换为表达式引擎参数 map[string]interface{}
  ↓
表达式计算
  ↓
Decimal 金额归一化与 rounding
  ↓
Fee Item append-only 落库
  ↓
保存 factor snapshot / rule snapshot / result snapshot
```

关键点：

```text
map[string]interface{} 只作为表达式引擎最后一公里适配层，不作为系统内部主模型。
```

---

## 6. 费用项规则设计

### 6.1 Fee Rule 职责

Fee Rule 负责定义：

```text
1. 什么业务场景需要计算这个费用项；
2. 费用项表达式是什么；
3. 依赖哪些因子；
4. 金额精度和舍入规则；
5. 规则版本和生效时间；
6. 是否启用；
7. 灰度范围。
```

Fee Rule 不负责：

```text
1. 具体因子如何取值；
2. 费率如何按时间匹配；
3. RPC 如何调用；
4. 表如何查询。
```

---

### 6.2 Fee Rule 配置示例

```yaml
fee_code: Fee103
fee_name: Merchant Service Fee
biz_scene: PAYMENT_SUCCESS
expression: "base_amount * service_fee_rate"

rounding:
  scale: 2
  mode: HALF_UP
  stage: FINAL_RESULT_ONLY

rule_version: 17
effective_from: "2026-05-01 00:00:00"
effective_to: null
status: ENABLED

depend_factors:
  - base_amount
  - service_fee_rate
```

---

## 7. 因子抽象设计

### 7.1 Factor Definition

因子是表达式中的变量，也是系统进行数据取值的最小单元。

建议核心字段：

```sql
factor_definition (
    id,
    factor_code,          -- base_amount / service_fee_rate
    factor_name,
    factor_type,          -- EVENT_FIELD / RPC / RULE_TABLE / TABLE_LOOKUP / EXPRESSION / CONSTANT
    factor_category,      -- RATE / AMOUNT / TIME / MERCHANT_ATTRIBUTE，可选
    data_type,            -- DECIMAL / STRING / INT / BOOL / DATETIME / STRUCT / LIST / MAP
    required,
    default_value,
    resolver_config,
    status,
    created_at,
    updated_at
)
```

---

### 7.1.1 FactorDataType

因子返回值不应只用 `interface{}` 表示，必须显式声明数据类型。

推荐类型集合：

```text
DECIMAL     高精度数值，金额、费率、税率等必须使用
STRING      字符串
INT         整数，例如数量、币种 scale、枚举 ID
BOOL        布尔
DATETIME    时间，统一要求 RFC3339 或标准 timestamp 表示
STRUCT      结构体对象，仅在明确声明时允许
LIST        列表，仅在明确声明时允许
MAP         键值映射，仅在明确声明时允许
```

约束：

```text
1. 金额相关字段禁止使用 FLOAT；
2. DECIMAL 是金额表达式中的主数值类型；
3. 复合类型不能直接参与金额计算，必须先通过子路径取值或派生出标量因子；
4. data_type 是因子契约的一部分，发布后变更需要走版本化治理。
```

---

### 7.1.2 FactorStatus

因子解析结果除了值本身，还必须显式表达状态。

推荐状态集合：

```text
OK           正常取到有效值
NULL         字段存在，但值为 null
MISSING      字段不存在或无结果
DEFAULTED    未取到原始值，已使用 default_value
INVALID      取到了值，但格式或类型不合法
ERROR        取值过程中发生错误，例如 RPC 超时、反序列化失败、规则冲突
```

说明：

```text
1. ZERO 不是状态，0 只是合法值；
2. NULL 与 MISSING 必须区分；
3. DEFAULTED 说明最终有值，但不能伪装成原始值；
4. ERROR 用于表示系统或依赖异常，和业务无值场景分开。
```

---

### 7.1.3 FactorValue

建议因子解析层统一返回 `FactorValue`，而不是裸值。

参考结构：

```go
type FactorDataType string

const (
    FactorDataTypeDecimal  FactorDataType = "DECIMAL"
    FactorDataTypeString   FactorDataType = "STRING"
    FactorDataTypeInt      FactorDataType = "INT"
    FactorDataTypeBool     FactorDataType = "BOOL"
    FactorDataTypeDatetime FactorDataType = "DATETIME"
    FactorDataTypeStruct   FactorDataType = "STRUCT"
    FactorDataTypeList     FactorDataType = "LIST"
    FactorDataTypeMap      FactorDataType = "MAP"
)

type FactorStatus string

const (
    FactorStatusOK        FactorStatus = "OK"
    FactorStatusNull      FactorStatus = "NULL"
    FactorStatusMissing   FactorStatus = "MISSING"
    FactorStatusDefaulted FactorStatus = "DEFAULTED"
    FactorStatusInvalid   FactorStatus = "INVALID"
    FactorStatusError     FactorStatus = "ERROR"
)

type FactorValue struct {
    FactorCode string
    FactorType string
    DataType   FactorDataType
    Status     FactorStatus
    Value      any
    ValueText  string
}
```

字段语义：

```text
FactorCode   因子编码，便于日志、快照、审计解释
FactorType   因子来源类型，例如 EVENT_FIELD / RPC / RULE_TABLE
DataType     返回值类型契约
Status       返回值状态契约
Value        运行时值，供因子系统和表达式适配层使用
ValueText    标准化字符串表示，供快照、审计、重算比对使用
```

落地要求：

```text
1. 表达式引擎只能消费 Status=OK 或 DEFAULTED 的标量因子；
2. Status=NULL / MISSING / INVALID / ERROR 时，不允许静默转成 0；
3. 因子快照建议保存 ValueText，而不是直接依赖运行时内存对象；
4. STRUCT / LIST / MAP 的 ValueText 推荐保存为稳定 JSON。
```

示例：

```json
{
  "factor_code": "service_fee_rate",
  "factor_type": "RULE_TABLE",
  "data_type": "DECIMAL",
  "status": "OK",
  "value": "0.20",
  "value_text": "0.20"
}
```

---

### 7.1.4 哪些因子允许返回复合类型

不是所有因子都应该允许返回 `STRUCT / LIST / MAP`。

推荐约束如下：

| factor_type | 是否允许复合类型 | 建议 |
|---|---|---|
| EVENT_FIELD | 有条件允许 | 可以从标准化事件中抽取 STRUCT / LIST / MAP，但必须显式声明 data_type，并限制路径和大小 |
| RPC | 有条件允许 | 可以返回 STRUCT / MAP，LIST 仅限受控场景；必须有 output_path 或字段映射，避免把整包响应直接暴露给表达式 |
| TABLE_LOOKUP | 默认不允许 | V1 只返回标量；如确需复合配置，建议拆成多个标量因子 |
| RULE_TABLE | 不允许 | 规则表返回值应是可审计、可比较的标量，例如费率、固定金额、阈值 |
| EXPRESSION | 不允许 | 表达式因子应只做标量派生，避免把表达式引擎变成对象编排器 |
| CONSTANT | 不允许 | 常量因子应只返回标量 |

进一步约束：

```text
1. 复合类型因子不能直接写入金额表达式；
2. 如果业务需要读取复合对象中的某个字段，应再派生一个标量因子；
3. RPC 返回复合对象时，必须配置白名单字段快照，避免审计快照无限膨胀；
4. LIST / MAP 因子应限制长度、层级和序列化大小；
5. Phase 1 建议只支持标量因子；复合类型优先只对 EVENT_FIELD / RPC 开放。
```

---

### 7.1.5 resolver_config 如何声明复合类型子路径

如果因子返回值是 `STRUCT / LIST / MAP`，`resolver_config` 不能只声明“取整个对象”，还必须声明：

```text
1. 原始来源路径；
2. 允许暴露给下游的子路径白名单；
3. 是否允许派生标量子因子；
4. 快照时哪些字段保留；
5. 大小限制。
```

推荐字段：

```yaml
factor_code: merchant_profile
factor_type: RPC
data_type: STRUCT
resolver_config:
  provider_code: GET_MERCHANT_PROFILE
  output_path: "$.data"
  subfields:
    - field_code: merchant_country
      source_path: "$.country"
      data_type: STRING
    - field_code: merchant_tier
      source_path: "$.tier"
      data_type: STRING
    - field_code: settlement_cycle_days
      source_path: "$.settlement.cycle_days"
      data_type: INT
  snapshot_fields:
    - "$.country"
    - "$.tier"
    - "$.settlement.cycle_days"
  max_depth: 3
  max_bytes: 4096
```

解释：

```text
subfields        允许从复合值继续派生标量因子
snapshot_fields  快照白名单，避免把整包大对象直接落库
max_depth        最大嵌套层级
max_bytes        序列化后的最大大小
```

约束：

```text
1. V1 不允许表达式直接写 merchant_profile.country 这类对象访问；
2. 如果表达式要用 country，必须通过 subfields 或单独因子派生出 merchant_country；
3. TABLE_LOOKUP / RULE_TABLE / EXPRESSION / CONSTANT 的 resolver_config 在 V1 不允许声明复合返回。
```

---

### 7.1.6 表达式引擎可消费的最终 Typed Factor Context

因子系统内部可以维护完整 `FactorValue`，但表达式引擎不应该直接消费整包复杂对象。

推荐分两层：

```text
Layer 1: Factor Resolve Result
  完整保留 FactorValue、Status、Source、快照信息

Layer 2: Expression Eval Context
  只暴露可计算的标量变量
```

参考结构：

```go
type TypedFactorContext struct {
    Factors map[string]FactorValue
}

type ExpressionEvalContext struct {
    Params map[string]any
}
```

转换规则：

```text
1. 只有 Status=OK / DEFAULTED 的标量因子才能进入 Params；
2. STRUCT / LIST / MAP 不直接进入 Params；
3. DECIMAL 进入表达式前统一转成受控数值表示；
4. DATETIME 不直接参与大小比较，除非平台显式提供时间函数或预派生时间因子；
5. NULL / MISSING / INVALID / ERROR 因子进入表达式前必须先失败或走显式默认值策略。
```

示例：

```json
{
  "factors": {
    "payment_amount": {
      "data_type": "DECIMAL",
      "status": "OK",
      "value_text": "100.00"
    },
    "merchant_profile": {
      "data_type": "STRUCT",
      "status": "OK",
      "value_text": "{\"country\":\"US\",\"tier\":\"NORMAL\"}"
    },
    "merchant_country": {
      "data_type": "STRING",
      "status": "OK",
      "value_text": "US"
    }
  }
}
```

最终传给表达式引擎的 `Params` 应该是：

```json
{
  "payment_amount": "100.00",
  "merchant_country": "US"
}
```

---

### 7.1.7 发布校验规则里要新增哪些校验项

在原有发布校验基础上，建议新增：

```text
1. 因子返回 data_type 是否与 resolver_config 一致；
2. default_value 是否能被解析成声明的 data_type；
3. 如果 data_type 是 STRUCT / LIST / MAP，是否声明了 subfields 或 snapshot_fields；
4. 复合类型因子是否错误地被费用表达式直接引用；
5. EXPRESSION 因子的输入因子是否全部为标量；
6. RPC / EVENT_FIELD 的 source_path 或 output_path 是否存在；
7. TABLE_LOOKUP 的 lookup_key 输入因子是否都是标量；
8. 表达式上下文里的所有变量最终是否都能转换成可计算标量；
9. DECIMAL / INT / STRING / DATETIME 的类型转换是否存在歧义；
10. 复合类型因子的 max_depth / max_bytes 是否超平台限制；
11. 因子快照字段是否会泄露超大对象或敏感字段；
12. 复合因子派生子字段时，field_code 是否与现有因子编码冲突。
```

---

### 7.2 EVENT_FIELD 因子

用于从标准化事件中取值。

示例：

```yaml
factor_code: payment_amount
factor_type: EVENT_FIELD
data_type: DECIMAL
resolver_config:
  source_path: "$.payment.amount"
```

---

### 7.3 RPC 因子

用于调用外部服务获取业务事实。

示例：

```yaml
factor_code: merchant_level
factor_type: RPC
data_type: STRING
resolver_config:
  provider_code: GET_MERCHANT_LEVEL
  method: MerchantService.GetMerchantLevel
  timeout_ms: 300
  input_mapping:
    merchant_id: merchant_id
    as_of_time: payment_success_time
  output_path: "$.data.level"
```

注意：

```text
RPC 因子必须支持 timeout、重试策略、失败策略、provider 白名单、请求响应摘要快照。
```

---

### 7.4 TABLE_LOOKUP 因子

用于基于唯一键精确查表。

适合：

```text
币种精度
商家本地配置
简单 key-value 映射
支付方式配置
```

示例：

```yaml
factor_code: currency_scale
factor_type: TABLE_LOOKUP
data_type: INT
resolver_config:
  table_code: currency_config
  lookup_key:
    currency: currency
  output_column: scale
```

底层等价于受控参数化查询：

```sql
select scale
from currency_config
where currency = ?
  and status = 'ACTIVE'
limit 1;
```

但业务方不直接配置 SQL。

---

### 7.5 RULE_TABLE 因子

用于规则表匹配，支持时间区间、维度匹配、通配符、优先级。

适合：

```text
费率
最低费用
最高费用
固定手续费
税率
结算周期
活动优惠率
```

示例：

```yaml
factor_code: service_fee_rate
factor_name: 商家服务费率
factor_type: RULE_TABLE
factor_category: RATE
data_type: DECIMAL
resolver_config:
  rule_table_code: RS_SERVICE_FEE_RATE
  output_column: rate_value
  input_mapping:
    country: country
    merchant_level: merchant_level
    payment_method: payment_method
    effective_time: payment_success_time
  no_match_strategy: FAIL
  conflict_strategy: ERROR_IF_SAME_PRIORITY_MULTI_MATCH
```

运行时语义：

```text
service_fee_rate = rule_table_lookup(
  table = RS_SERVICE_FEE_RATE,
  dimensions = country, merchant_level, payment_method,
  effective_time = payment_success_time,
  output = rate_value
)
```

该逻辑由平台内置，不允许业务方写 if/else。

---

### 7.6 EXPRESSION 因子

用于由其他因子派生。

示例：

```yaml
factor_code: base_amount
factor_type: EXPRESSION
data_type: DECIMAL
resolver_config:
  expression: "payment_amount - discount_amount"
  depend_factors:
    - payment_amount
    - discount_amount
```

注意：

```text
EXPRESSION 因子也需要经过类型校验和 Decimal 处理。
```

---

### 7.7 CONSTANT 因子

用于固定常量。

示例：

```yaml
factor_code: platform_rate_default
factor_type: CONSTANT
data_type: DECIMAL
resolver_config:
  value: "0.20"
```

---

## 8. 规则表设计

### 8.1 RULE_TABLE 与 TABLE_LOOKUP 的区别

| 类型 | 适用场景 | 查询方式 | 是否依赖唯一键 |
|---|---|---|---|
| TABLE_LOOKUP | 精确查表 | key-value 查询 | 是 |
| RULE_TABLE | 费率、税率、封顶、保底等规则匹配 | 维度 + 时间 + 通配符 + 优先级 | 不一定，依赖发布前冲突检测 |

---

### 8.2 规则表元数据

```sql
rule_table (
    id,
    rule_table_code,
    rule_table_name,
    version,
    conflict_strategy,
    no_match_strategy,
    status,
    created_at,
    updated_at
)
```

---

### 8.3 规则表维度定义

```sql
rule_table_dimension (
    id,
    rule_table_id,
    dimension_code,     -- country / merchant_level / payment_method / effective_time
    data_type,
    match_operator,     -- EQ / IN / RANGE / EQ_OR_WILDCARD
    required,
    created_at
)
```

---

### 8.4 规则表行

```sql
rule_table_row (
    id,
    rule_table_id,
    rule_code,
    condition_json,
    output_json,
    priority,
    effective_from,
    effective_to,
    status,
    created_at,
    updated_at
)
```

`condition_json` 示例：

```json
{
  "country": "US",
  "merchant_level": "NORMAL",
  "payment_method": "CARD"
}
```

`output_json` 示例：

```json
{
  "rate_value": "0.20",
  "min_fee": "0.30",
  "max_fee": "100.00"
}
```

---

### 8.5 服务费率规则表示例

| country | merchant_level | payment_method | effective_from | effective_to | rate_value | priority |
|---|---|---|---|---|---:|---:|
| US | NORMAL | CARD | 2026-05-01 | 2026-06-01 | 0.20 | 10 |
| US | NORMAL | CARD | 2026-06-01 | - | 0.18 | 10 |
| US | VIP | CARD | 2026-05-01 | - | 0.15 | 20 |
| * | * | * | 2026-05-01 | - | 0.25 | 1 |

匹配逻辑：

```text
1. 根据 effective_time 过滤生效区间：
   effective_from <= effective_time < effective_to

2. 根据维度过滤：
   country / merchant_level / payment_method

3. 支持 * 通配符；

4. 多条命中时按 priority 降序选择；

5. 如果最高 priority 下仍有多条命中，报配置冲突；

6. 如果没有命中，根据 no_match_strategy 决定失败、默认值或人工处理。
```

---

## 9. 表达式引擎适用方式

### 9.1 表达式引擎职责

表达式引擎只负责：

```text
1. 使用已解析因子进行纯计算；
2. 支持加减乘除；
3. 支持 min/max/abs/round 等受控函数；
4. 返回原始计算结果。
```

不负责：

```text
1. 解析 MQ 消息；
2. 查询 RPC；
3. 查询数据库；
4. 匹配费率规则；
5. 处理时间生效逻辑；
6. 推断默认值；
7. 处理字段缺失。
```

---

### 9.2 Govaluate 使用边界

Govaluate 可以作为表达式引擎，但需要注意：

```text
1. 它天然使用 map[string]interface{} 作为参数输入；
2. 数字计算容易进入 float64；
3. 金融金额计算不能直接依赖 float64；
4. 需要封装 Decimal 函数或使用 minor unit；
5. 表达式应在规则发布或加载时预编译并缓存；
6. 不应每条 MQ 消息都重新 parse 表达式。
```

建议封装一层：

```text
BillingExpressionEngine
  ↓
Govaluate Adapter
```

避免业务规则直接绑定 Govaluate 语法。

---

### 9.3 map[string]interface{} 的定位

数据链路中不建议直接：

```text
MQ String -> map[string]interface{} -> Govaluate
```

推荐：

```text
MQ String
  -> Strong Typed Event
  -> NormalizedEvent
  -> Typed Factor Context
  -> controlled map[string]interface{}
  -> Govaluate
```

`map[string]interface{}` 只允许作为最后一公里适配层。

---

## 10. 运行时执行流程

### 10.1 消息接入

```text
1. 从 MQ 获取原始字符串；
2. 反序列化成强类型事件；
3. 校验 schema_version、event_id、biz_scene；
4. 标准化成 NormalizedEvent；
5. 生成 idempotency_key；
6. 保存原始消息或原始消息引用。
```

---

### 10.2 规则匹配

```text
1. 根据 biz_scene 匹配候选 Fee Rule；
2. 根据国家、商家、类目、支付方式等条件过滤；
3. 根据 event biz_time 匹配 Fee Rule 生效版本；
4. 得到本次需要计算的费用项列表。
```

---

### 10.3 因子解析

```text
1. 提取所有费用项表达式依赖因子；
2. 根据 factor_definition 构建依赖关系；
3. 检查循环依赖；
4. 按 DAG 拓扑顺序解析；
5. 同一事件内相同因子只解析一次；
6. 可并行的因子并发解析；
7. 生成 Typed Calculation Context。
```

---

### 10.4 表达式计算

```text
1. 将 Typed Factor Context 受控转换为表达式参数；
2. 使用预编译表达式计算；
3. 对结果做 Decimal 归一化；
4. 按 Fee Rule 配置执行 rounding；
5. 得到最终金额。
```

---

### 10.5 费用项落库

```text
1. 使用幂等键插入费用项；
2. 保存费用项金额；
3. 保存规则版本；
4. 保存表达式；
5. 保存因子快照；
6. 保存规则表命中记录；
7. 保存计算状态。
```

---

## 11. 核心表设计

### 11.1 原始事件表 billing_event

```sql
billing_event (
    id,
    event_id,
    event_type,
    biz_scene,
    biz_order_id,
    merchant_id,
    biz_time,
    raw_message,
    message_hash,
    schema_version,
    status,
    created_at,
    updated_at,
    unique key uk_event_id (event_id)
)
```

---

### 11.2 费用项表 fee_item

建议 append-only，不直接覆盖历史费用。

```sql
fee_item (
    id,
    event_id,
    biz_order_id,
    payment_order_id,
    refund_order_id,
    merchant_id,
    fee_code,
    fee_direction,       -- CHARGE / REFUND / REVERSAL
    amount,
    currency,
    rule_id,
    rule_version,
    expression,
    factor_snapshot_id,
    result_snapshot,
    idempotency_key,
    calc_status,
    created_at,
    updated_at,
    unique key uk_idempotency_key (idempotency_key)
)
```

---

### 11.3 因子快照表 factor_snapshot

```sql
factor_snapshot (
    id,
    event_id,
    fee_item_id,
    fee_code,
    rule_id,
    rule_version,
    snapshot_json,
    snapshot_hash,
    created_at
)
```

`snapshot_json` 示例：

```json
{
  "base_amount": {
    "factor_type": "EVENT_FIELD",
    "data_type": "DECIMAL",
    "status": "OK",
    "source_path": "$.payment.amount",
    "value": "100.00",
    "value_text": "100.00"
  },
  "service_fee_rate": {
    "factor_type": "RULE_TABLE",
    "data_type": "DECIMAL",
    "status": "OK",
    "rule_table_code": "RS_SERVICE_FEE_RATE",
    "matched_row_id": "RR_US_NORMAL_CARD_202605",
    "matched_by_time": "2026-05-15 10:30:00",
    "value": "0.20",
    "value_text": "0.20"
  }
}
```

---

### 11.4 规则相关表

```sql
fee_rule (
    id,
    fee_code,
    fee_name,
    biz_scene,
    expression,
    match_condition,
    rounding_config,
    rule_version,
    effective_from,
    effective_to,
    status,
    created_by,
    approved_by,
    published_at,
    created_at,
    updated_at
)
```

```sql
factor_definition (
    id,
    factor_code,
    factor_name,
    factor_type,
    factor_category,
    data_type,
    required,
    default_value,
    resolver_config,
    status,
    created_at,
    updated_at
)
```

```sql
rule_table (
    id,
    rule_table_code,
    rule_table_name,
    version,
    conflict_strategy,
    no_match_strategy,
    status,
    created_at,
    updated_at
)
```

```sql
rule_table_row (
    id,
    rule_table_id,
    rule_code,
    condition_json,
    output_json,
    priority,
    effective_from,
    effective_to,
    status,
    created_at,
    updated_at
)
```

---

## 12. 幂等与一致性

### 12.1 MQ 消费幂等

建议使用：

```text
event_id
```

作为事件幂等基础。

如果同一事件可能产生多个费用项，则费用项幂等键可以是：

```text
event_id + fee_code + fee_direction + rule_version
```

或者：

```text
biz_order_id + event_type + fee_code + fee_direction
```

具体取决于上游事件语义。

---

### 12.2 费用项 append-only

支付成功产生正向费用：

```text
Fee103 +20.00
```

退款冲正不建议 update 原费用，而是新增负向费用：

```text
Fee103 -20.00
```

这样可以保证账务链路完整。

---

### 12.3 退款费率语义

退款场景需要区分：

```text
1. 对原支付费用的冲正：
   应引用原费用项快照中的费率，而不是用退款时间重新匹配费率。

2. 退款本身产生新的手续费：
   可以使用 refund_success_time 匹配 refund_fee_rate。
```

---

## 13. 异常处理

### 13.1 计算状态

```text
RECEIVED
FACTOR_RESOLVING
CALCULATING
CALCULATED
PERSISTED
FAILED_RETRYABLE
FAILED_FINAL
MANUAL_REVIEW
```

---

### 13.2 常见异常

| 异常 | 建议处理 |
|---|---|
| MQ 反序列化失败 | FAILED_FINAL / DEAD LETTER |
| 必填字段缺失 | FAILED_FINAL / MANUAL_REVIEW |
| RPC 超时 | FAILED_RETRYABLE |
| RPC 返回业务不存在 | FAILED_FINAL / MANUAL_REVIEW |
| RULE_TABLE 无命中 | 根据 no_match_strategy |
| RULE_TABLE 多条冲突 | FAILED_FINAL，配置修复后重放 |
| 表达式计算失败 | FAILED_FINAL |
| 金额精度异常 | FAILED_FINAL |
| 重复消息 | 幂等忽略或返回已有结果 |

---

### 13.3 默认值策略

不允许系统默认把缺失值当成 0。

只有因子配置明确声明时才允许：

```yaml
factor_code: discount_amount
required: false
default_value: "0"
```

必须区分：

```text
MISSING     字段不存在
NULL        字段存在但为 null
ZERO        字段存在且为 0
INVALID     字段格式错误
```

---

## 14. 规则发布与配置平台

### 14.1 配置平台页面

建议至少包括：

```text
1. 费用项规则配置页面；
2. 因子定义页面；
3. 规则表配置页面；
4. RPC Provider 注册页面；
5. 模拟试算页面；
6. 规则版本历史页面；
7. 审批发布页面；
8. 回滚页面；
9. 计算解释页面。
```

---

### 14.2 发布前校验

发布 Fee Rule 或 Factor Definition 前，需要校验：

```text
1. 表达式语法是否合法；
2. 表达式变量是否都有对应因子；
3. 因子 data_type 是否兼容；
4. 因子依赖是否有环；
5. RULE_TABLE 是否存在；
6. output_column 是否存在；
7. RPC Provider 是否存在；
8. 金额 rounding 配置是否完整；
9. 样例数据是否能试算通过；
10. default_value 是否匹配 data_type；
11. 复合类型因子是否声明 subfields / snapshot_fields；
12. 复合类型因子是否被表达式直接引用；
13. lookup_key / input_mapping 依赖因子是否全部为标量；
14. 复合类型大小限制是否配置完整；
15. 因子快照字段是否超白名单范围。
```

发布 Rule Table 前，需要校验：

```text
1. 时间区间是否合法；
2. 同一 priority 下是否存在多条命中冲突；
3. output_json 字段类型是否正确；
4. 是否有默认规则；
5. 是否存在无法覆盖的重要场景；
6. 是否影响历史规则版本。
```

---

### 14.3 模拟试算

配置平台必须支持模拟试算。

输入：

```text
payment_amount = 100
country = US
merchant_level = NORMAL
payment_method = CARD
payment_success_time = 2026-05-15
```

输出：

```text
base_amount = 100
service_fee_rate = 0.20
Fee103 = 20.00
命中规则行：RR_US_NORMAL_CARD_202605
规则版本：17
```

---

## 15. 可观测与审计

### 15.1 日志

每次计算至少记录：

```text
event_id
biz_order_id
merchant_id
fee_code
rule_version
factor_resolve_cost
expression_eval_cost
final_amount
calc_status
error_code
```

---

### 15.2 指标

建议监控：

```text
1. MQ 消费延迟；
2. 计费成功率；
3. 因子解析失败率；
4. RPC 因子超时率；
5. RULE_TABLE 无命中率；
6. RULE_TABLE 冲突率；
7. 表达式计算失败率；
8. 单事件费用项数量；
9. 单事件因子数量；
10. 平均计算耗时和 P99 耗时。
```

---

### 15.3 审计解释

需要支持按订单查看：

```text
1. 原始消息；
2. 标准化事件；
3. 命中的 Fee Rule；
4. 命中的 Rule Table 行；
5. 每个因子的来源和值；
6. 表达式；
7. 计算结果；
8. 舍入过程；
9. 最终费用项。
```

---

## 16. 性能设计

### 16.1 表达式预编译

表达式应在规则加载时解析并缓存：

```text
cache_key = fee_code + rule_version + expression_hash
```

运行时直接 Evaluate。

---

### 16.2 因子去重

同一个事件内，如果多个费用项依赖相同因子，只解析一次。

例如：

```text
Fee103 依赖 merchant_level
Fee104 也依赖 merchant_level
```

则 `merchant_level` RPC 只调用一次。

---

### 16.3 规则表匹配缓存

对于同一个：

```text
rule_table_code + input_mapping values + effective_time
```

可以在单次事件上下文内缓存匹配结果。

如果一张规则表输出多个字段，例如：

```text
rate_value
min_fee
max_fee
```

多个因子可以复用同一次规则表匹配结果。

---

### 16.4 RPC 因子治理

```text
1. 设置 timeout；
2. 设置最大并发；
3. 设置重试策略；
4. 支持 bulk provider；
5. 同一事件内去重；
6. 记录 provider 维度耗时和错误率。
```

---

## 17. 关键风险

### 17.1 金额精度风险

Govaluate 可能将数字处理为 float64，不能直接用于金融金额。

需要确认：

```text
1. 是否使用 Decimal 函数封装；
2. 是否使用 minor unit int64；
3. 是否更换支持 Decimal 的表达式方案；
4. 中间过程和最终结果如何 rounding。
```

---

### 17.2 规则冲突风险

RULE_TABLE 如果配置错误，可能多条命中。

必须通过发布前校验和运行时保护解决。

---

### 17.3 历史重算风险

如果 RPC 因子或规则表使用当前值，而不是 biz_time/as_of_time 对应值，会导致历史订单重算不一致。

必须明确：

```text
所有历史重算都基于事件业务时间、规则版本、因子快照或 as-of-time 查询。
```

---

### 17.4 map[string]interface{} 类型丢失风险

`map[string]interface{}` 只作为表达式引擎输入适配层。

系统内部必须保持：

```text
Typed Factor Context
```

---

## 18. 待确认问题点

### 18.1 业务语义

```text
1. Fee103 具体代表什么费用？服务费、佣金、支付手续费，还是其他？
2. 支付、退款、部分退款、撤销、拒付是否都会进入该计费系统？
3. 退款是冲正原费用，还是产生新的退款手续费？
4. 计费基数 base_amount 应该来自支付金额、结算金额、净额还是其他字段？
5. 优惠、折扣、税费是否参与计费基数？
6. 多币种场景下按交易币种计费，还是统一换算为结算币种？
```

---

### 18.2 事件模型

```text
1. MQ 消息是否有全局唯一 event_id？
2. 是否有 schema_version？
3. 支付时间、退款时间、订单创建时间哪个作为 biz_time？
4. 消息是否可能乱序？
5. 支付成功和退款成功事件是否可能重复投递？
6. 是否需要保存完整原始消息？保存多久？
```

---

### 18.3 规则与版本

```text
1. Fee Rule 的生效时间用 payment_time、event_time 还是消费时间？
2. Rule Table 的版本是否和 Fee Rule 版本绑定？
3. 已发布规则是否允许修改？还是只能新建版本？
4. 是否需要审批流？谁审批？财务、运营、研发？
5. 是否需要按 merchant/country 灰度？
6. 规则回滚如何影响已计算费用？
```

---

### 18.4 因子系统

```text
1. 因子是否允许跨费用项复用？
2. 因子定义是全局唯一，还是按 biz_scene 隔离？
3. EVENT_FIELD 因子的 source_path 是否基于标准化事件，而不是原始 MQ JSON？
4. RPC 因子是否必须支持 as_of_time？
5. TABLE_LOOKUP 是否只允许查平台注册表？
6. RULE_TABLE 是否支持 IN、范围、通配符、优先级？
7. 无命中时默认失败还是允许默认值？
```

---

### 18.5 表达式引擎

```text
1. Govaluate 是否满足 Decimal 金额计算要求？
2. 是否允许使用自定义函数 mul/add/sub/div/round？
3. 是否允许条件表达式？如果允许，边界是什么？
4. 是否允许字符串比较、布尔逻辑？
5. 表达式变量名规范是什么？
6. 表达式是否需要预编译和缓存？
7. 是否需要评估替代表达式引擎，例如 Expr、CEL 或自研 DSL？
```

---

### 18.6 数据库与性能

```text
1. fee_item 预计日增量多少？
2. 是否需要按 merchant_id、biz_time 分库分表？
3. factor_snapshot 是否单独大字段存储？
4. raw_message 是否进入冷存储？
5. RULE_TABLE 匹配是否需要本地缓存？
6. RPC 因子是否支持批量调用？
7. 单个事件最多可能产生多少费用项？
```

---

### 18.7 对账与审计

```text
1. 是否需要和交易系统、支付渠道、财务账单系统对账？
2. 是否需要支持订单级重算？
3. 是否需要支持批量历史重算？
4. 重算结果如果和原结果不一致，如何处理？
5. 是否需要自动生成调整费用项？
6. 审计查询需要保存多久？
```

---

## 19. 推荐分期方案

### 19.1 Phase 1：基础计费引擎

```text
1. MQ 消费与标准化事件；
2. Fee Rule 配置；
3. EVENT_FIELD 因子；
4. CONSTANT 因子；
5. 简单 RULE_TABLE 因子；
6. 表达式计算；
7. fee_item 落库；
8. factor_snapshot；
9. 幂等处理；
10. 模拟试算。
```

---

### 19.2 Phase 2：复杂因子与规则治理

```text
1. RPC 因子；
2. TABLE_LOOKUP 因子；
3. RULE_TABLE 冲突检测；
4. 规则审批发布；
5. 规则版本和回滚；
6. 多费用项共享因子；
7. 表达式预编译缓存。
```

---

### 19.3 Phase 3：审计、重算与运营化

```text
1. 计算解释页面；
2. 历史订单重算；
3. 差异对比；
4. 人工处理队列；
5. 批量补偿任务；
6. 对账报表；
7. 性能优化与缓存。
```

---

## 20. 总结

本方案的核心不是简单地使用 Govaluate 计算表达式，而是建设一套完整的计费规则平台。

核心抽象如下：

```text
Fee Rule
  定义费用项、表达式、生效版本和 rounding。

Factor Definition
  定义表达式变量如何取值。

Factor Resolver
  根据因子类型执行 EVENT_FIELD / RPC / TABLE_LOOKUP / RULE_TABLE / EXPRESSION 等取值逻辑。

Rule Table
  用表格方式表达费率、税率、封顶、保底等规则，避免业务方写 if/else。

Expression Engine
  只做纯计算。

Fee Item
  append-only 落库，保存金额、规则版本和因子快照。
```

最关键的设计边界是：

```text
1. 费率也是因子，但执行类型应是 RULE_TABLE，而不是 RATE；
2. 因子类型按数据获取逻辑划分，而不是按业务语义划分；
3. 表达式引擎只做纯计算，不做取数和规则匹配；
4. map[string]interface{} 只作为表达式引擎适配层，系统内部保持 Typed Factor Context；
5. 金额计算必须避免 float64；
6. 所有计算必须保存可审计快照；
7. 历史规则不可直接覆盖，应该版本化和 append-only。
```

一句话概括：

```text
这是一个以 Fee Rule 为入口、以 Factor Resolver 为取数核心、以 Rule Table 替代脚本 if/else、以表达式引擎完成纯计算、以快照和版本保障审计与重算的商家结算计费平台。
```
