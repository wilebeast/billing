# Golang 因子 Fetch 接口与 FactorValue 设计方案

## 1. 文档背景

在计费系统中，每个费用项表达式依赖一组因子，例如：

```text
Fee103 = base_amount * service_fee_rate
```

其中：

```text
base_amount          可能来自 MQ 消息字段
service_fee_rate     可能来自规则表匹配
merchant_level       可能来自 RPC 泛化调用
currency_scale       可能来自本地配置表查询
```

因此系统需要一套统一的因子解析机制，用来完成：

```text
1. 根据因子配置获取数据；
2. 支持不同取数方式；
3. 支持因子之间的依赖；
4. 统一返回因子结果；
5. 支持表达式计算；
6. 支持审计快照；
7. 支持错误处理和历史重算。
```

本文重点设计两个核心部分：

```text
1. 因子 Fetch / Resolve 接口如何设计；
2. FactorValue 因子结果对象如何设计。
```

---

## 2. 核心结论

不建议设计成：

```go
type FactorFetcher interface {
    Fetch(ctx context.Context, input interface{}) (interface{}, error)
}
```

因为这种接口输入、输出都依赖 `interface{}`，短期灵活，但长期会导致：

```text
1. input 里有什么不清楚；
2. output 是什么类型不清楚；
3. 无法静态分析因子依赖；
4. 无法做发布前校验；
5. 无法构建因子 DAG；
6. 类型错误只能运行时发现；
7. 无法稳定生成快照；
8. 审计和重算困难；
9. 系统会逐渐退化成动态脚本系统。
```

推荐设计成：

```text
FactorDefinition 是配置；
FactorResolver 是按 factor_type 实现的解析器；
ResolveRequest 是强类型输入上下文；
FactorValue 是统一输出结果；
interface{} 只允许被限制在 Value/RawValue 和少数适配层中。
```

最终推荐接口：

```go
type FactorResolver interface {
    Type() FactorType
    Validate(def FactorDefinition, catalog FactorCatalog) error
    Dependencies(def FactorDefinition) ([]FactorCode, error)
    Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error)
}
```

---

## 3. 为什么不建议每个因子都实现 Fetch 接口

### 3.1 不推荐模型

```go
type Factor interface {
    Fetch(ctx context.Context) (any, error)
}

type ServiceFeeRateFactor struct{}
type BaseAmountFactor struct{}
type MerchantLevelFactor struct{}
```

这种方式的问题是：

```text
1. 每新增一个业务因子，都需要新增 Go 代码；
2. 因子数量多后会出现类爆炸；
3. 不利于配置平台动态新增因子；
4. 无法复用 EVENT_FIELD / RPC / RULE_TABLE 这些通用取数逻辑；
5. 因子定义和执行逻辑耦合；
6. 不适合“完全配置化”的计费系统。
```

---

### 3.2 推荐模型

将“业务因子”和“取数逻辑”分开。

```text
service_fee_rate 是一个业务因子配置；
它的 factor_type = RULE_TABLE；
真正执行取数逻辑的是 RuleTableResolver。
```

也就是说：

```text
不同业务因子：配置数据；
不同取数方式：代码实现。
```

例如：

```text
payment_amount       factor_type = EVENT_FIELD
service_fee_rate     factor_type = RULE_TABLE
merchant_level       factor_type = RPC
currency_scale       factor_type = TABLE_LOOKUP
base_amount          factor_type = EXPRESSION
```

代码层面只需要实现这些通用 Resolver：

```text
EventFieldResolver
RuleTableResolver
RPCResolver
TableLookupResolver
ExpressionResolver
ConstantResolver
```

---

## 4. 因子类型设计

因子类型应该按照“数据获取逻辑”划分，而不是按照业务含义划分。

```go
type FactorType string

const (
    FactorTypeEventField  FactorType = "EVENT_FIELD"
    FactorTypeRPC         FactorType = "RPC"
    FactorTypeTableLookup FactorType = "TABLE_LOOKUP"
    FactorTypeRuleTable   FactorType = "RULE_TABLE"
    FactorTypeExpression  FactorType = "EXPRESSION"
    FactorTypeConstant    FactorType = "CONSTANT"
)
```

说明：

| FactorType | 含义 | 示例 |
|---|---|---|
| EVENT_FIELD | 从标准化事件字段取值 | payment_amount |
| RPC | 调外部服务取值 | merchant_level |
| TABLE_LOOKUP | 基于唯一键查表 | currency_scale |
| RULE_TABLE | 规则表匹配取值 | service_fee_rate |
| EXPRESSION | 由其他因子派生 | base_amount |
| CONSTANT | 固定常量 | default_rate |

注意：

```text
service_fee_rate 在业务上是费率，但在执行上是 RULE_TABLE 类型。
```

可以额外增加业务分类字段：

```text
factor_category = RATE / AMOUNT / TIME / MERCHANT_ATTRIBUTE
```

但 `factor_category` 只用于展示、搜索、权限和管理，不参与执行分派。

---

## 5. 因子数据类型设计

因子数据类型表示因子结果的业务类型，不等于 Go 原生类型。

```go
type FactorDataType string

const (
    DataTypeDecimal  FactorDataType = "DECIMAL"
    DataTypeString   FactorDataType = "STRING"
    DataTypeInt64    FactorDataType = "INT64"
    DataTypeBool     FactorDataType = "BOOL"
    DataTypeDateTime FactorDataType = "DATETIME"
    DataTypeObject   FactorDataType = "OBJECT"
    DataTypeArray    FactorDataType = "ARRAY"
)
```

推荐约定：

| FactorDataType | Go 内部推荐类型 |
|---|---|
| DECIMAL | decimal.Decimal |
| STRING | string |
| INT64 | int64 |
| BOOL | bool |
| DATETIME | time.Time |
| OBJECT | map[string]any 或结构化对象 |
| ARRAY | []any 或结构化数组 |

金额、费率等金融相关字段推荐使用：

```text
decimal.Decimal
```

不要使用：

```text
float64
```

---

## 6. FactorDefinition 设计

`FactorDefinition` 是因子的配置定义，不负责具体执行逻辑。

```go
type FactorCode string

type FactorDefinition struct {
    Code           FactorCode     `json:"code"`
    Name           string         `json:"name"`
    Type           FactorType     `json:"type"`
    DataType       FactorDataType `json:"data_type"`
    Required       bool           `json:"required"`

    // 业务分类，仅用于管理和展示
    Category       string         `json:"category,omitempty"`

    // 不同 FactorType 的具体配置
    ResolverConfig json.RawMessage `json:"resolver_config"`

    DefaultValue   *string        `json:"default_value,omitempty"`
    Version        int64          `json:"version"`
}
```

示例：规则表费率因子 `service_fee_rate`。

```json
{
  "code": "service_fee_rate",
  "name": "商家服务费率",
  "type": "RULE_TABLE",
  "data_type": "DECIMAL",
  "required": true,
  "category": "RATE",
  "resolver_config": {
    "rule_table_code": "RS_SERVICE_FEE_RATE",
    "output_column": "rate_value",
    "input_mapping": {
      "country": "country",
      "merchant_level": "merchant_level",
      "payment_method": "payment_method",
      "effective_time": "payment_success_time"
    }
  }
}
```

---

## 7. Fetch 接口设计原则

### 7.1 不推荐接口

```go
type FactorFetcher interface {
    Fetch(ctx context.Context, input interface{}) (interface{}, error)
}
```

或者：

```go
type FactorFetcher interface {
    Fetch(ctx context.Context, input map[string]interface{}) (interface{}, error)
}
```

问题：

```text
1. input 没有明确结构；
2. output 没有明确类型；
3. Resolver 内部充满类型断言；
4. 缺少依赖声明；
5. 无法做 DAG；
6. 无法做发布前校验；
7. 无法统一快照和审计；
8. 错误处理不规范。
```

---

### 7.2 推荐接口：FactorResolver

```go
type FactorResolver interface {
    // 返回当前 Resolver 支持的 FactorType
    Type() FactorType

    // 发布规则时调用，用于校验配置是否合法
    Validate(def FactorDefinition, catalog FactorCatalog) error

    // 构建 DAG 时调用，用于分析当前因子依赖哪些其他因子
    Dependencies(def FactorDefinition) ([]FactorCode, error)

    // 运行时调用，真正解析因子值
    Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error)
}
```

三个阶段的职责：

| 方法 | 阶段 | 作用 |
|---|---|---|
| Validate | 发布前 | 校验因子配置是否合法 |
| Dependencies | 发布前 / 运行前 | 分析因子依赖，构建 DAG |
| Resolve | 运行时 | 获取因子结果 |

---

## 8. ResolveRequest 设计

`ResolveRequest` 是因子解析器的强类型输入上下文。

```go
type ResolveRequest struct {
    Event     NormalizedEvent
    Factor   FactorDefinition
    Values   FactorValueStore
    RuleCtx  RuleContext
    CalcMeta CalculationMeta
}
```

### 8.1 NormalizedEvent

标准化后的事件对象，隔离 MQ 原始 JSON 和不同 schema version。

```go
type NormalizedEvent interface {
    GetByPath(path string) (any, bool)
    EventID() string
    EventType() string
    BizTime() time.Time
}
```

---

### 8.2 RuleContext

当前费用项规则上下文。

```go
type RuleContext struct {
    FeeCode     string
    RuleID      string
    RuleVersion int64
    BizScene    string
}
```

---

### 8.3 CalculationMeta

本次计算上下文。

```go
type CalculationMeta struct {
    EventID    string
    MerchantID string
    BizOrderID string
    BizTime    time.Time
    TraceID    string
}
```

---

## 9. FactorValueStore 设计

因子之间存在依赖关系，例如：

```text
service_fee_rate 依赖 country、merchant_level、payment_method、payment_success_time
merchant_level 依赖 merchant_id
```

因此 Resolver 需要读取已经解析好的依赖因子。

不建议直接暴露：

```go
map[string]interface{}
```

推荐封装为：

```go
type FactorValueStore interface {
    Get(code FactorCode) (FactorValue, bool)
    Set(code FactorCode, value FactorValue)
    All() map[FactorCode]FactorValue

    GetString(code FactorCode) (string, error)
    GetInt64(code FactorCode) (int64, error)
    GetDecimal(code FactorCode) (decimal.Decimal, error)
    GetBool(code FactorCode) (bool, error)
    GetTime(code FactorCode) (time.Time, error)
}
```

示例：

```go
country, err := req.Values.GetString("country")
if err != nil {
    return NewFailedFactorValue(
        req.Factor.Code,
        req.Factor.DataType,
        FactorSource{FactorType: FactorTypeRuleTable},
        "DEPENDENCY_ERROR",
        err.Error(),
    ), nil
}
```

这样可以避免到处写：

```go
country := values["country"].(string)
```

---

## 10. FactorValue 设计目标

`FactorValue` 是因子解析后的统一结果对象。

它不应该只是裸值：

```go
type FactorValue struct {
    Value any
}
```

它应该承载：

```text
1. 这个因子是谁；
2. 这个因子的业务数据类型；
3. 这个因子的解析状态；
4. 这个因子的规范化值；
5. 这个因子的原始值；
6. 这个因子的来源信息；
7. 错误码和错误信息；
8. 审计和快照所需元数据。
```

核心原则：

```text
FactorValue 的价值不是保存 any，而是把 any 包在一个强约束壳子里。
```

---

## 11. FactorStatus 设计

```go
type FactorStatus string

const (
    FactorStatusOK      FactorStatus = "OK"
    FactorStatusMissing FactorStatus = "MISSING"
    FactorStatusNull    FactorStatus = "NULL"
    FactorStatusInvalid FactorStatus = "INVALID"
    FactorStatusFailed  FactorStatus = "FAILED"
)
```

状态含义：

| 状态 | 含义 |
|---|---|
| OK | 解析成功 |
| MISSING | 字段不存在 |
| NULL | 字段存在但为 null |
| INVALID | 字段存在但类型或格式非法 |
| FAILED | 取数失败，例如 RPC 业务失败、规则表无命中 |

需要明确区分：

```text
字段不存在 ≠ 字段为 null ≠ 字段值为 0
```

是否允许默认值，必须由 `FactorDefinition.DefaultValue` 决定。

---

## 12. FactorValue 推荐结构

```go
type FactorValue struct {
    Code     FactorCode     `json:"code"`
    DataType FactorDataType `json:"data_type"`
    Status   FactorStatus   `json:"status"`

    // 规范化后的值，给后续因子、规则表、表达式引擎使用。
    // DECIMAL  -> decimal.Decimal
    // STRING   -> string
    // INT64    -> int64
    // BOOL     -> bool
    // DATETIME -> time.Time
    // OBJECT   -> map[string]any
    // ARRAY    -> []any
    Value any `json:"-"`

    // 原始值，用于审计和排查。
    RawValue any `json:"-"`

    // 来源信息
    Source FactorSource `json:"source"`

    // 业务级解析失败错误
    ErrorCode    string `json:"error_code,omitempty"`
    ErrorMessage string `json:"error_message,omitempty"`

    // 扩展信息，例如耗时、provider trace id、规则版本
    Extra map[string]any `json:"extra,omitempty"`
}
```

说明：

```text
Value 是规范化后的系统内部值；
RawValue 是原始输入值；
Source 是审计来源；
ErrorCode/ErrorMessage 用于业务失败；
系统级错误仍通过 Go error 返回。
```

---

## 13. FactorSource 设计

```go
type FactorSource struct {
    FactorType FactorType `json:"factor_type"`

    // EVENT_FIELD
    SourcePath string `json:"source_path,omitempty"`

    // RPC
    ProviderCode string         `json:"provider_code,omitempty"`
    Method       string         `json:"method,omitempty"`
    Request      map[string]any `json:"request,omitempty"`
    ResponsePath string         `json:"response_path,omitempty"`

    // TABLE_LOOKUP
    TableCode string         `json:"table_code,omitempty"`
    LookupKey map[string]any `json:"lookup_key,omitempty"`

    // RULE_TABLE
    RuleTableCode string         `json:"rule_table_code,omitempty"`
    MatchedRowID  string         `json:"matched_row_id,omitempty"`
    MatchedInputs map[string]any `json:"matched_inputs,omitempty"`

    // EXPRESSION
    Expression string `json:"expression,omitempty"`

    // CONSTANT
    ConstantValue any `json:"constant_value,omitempty"`

    // 通用字段
    Version int64 `json:"version,omitempty"`
    CostMs  int64 `json:"cost_ms,omitempty"`
}
```

示例：`service_fee_rate` 来源信息。

```json
{
  "factor_type": "RULE_TABLE",
  "rule_table_code": "RS_SERVICE_FEE_RATE",
  "matched_row_id": "RR_US_NORMAL_CARD_202605",
  "matched_inputs": {
    "country": "US",
    "merchant_level": "NORMAL",
    "payment_method": "CARD",
    "effective_time": "2026-05-15T10:30:00Z"
  },
  "version": 12,
  "cost_ms": 3
}
```

---

## 14. Value 和 RawValue 的区别

例如 MQ 原始字段是：

```json
{
  "payment_amount": "100.23"
}
```

解析后：

```go
RawValue = "100.23"
Value    = decimal.RequireFromString("100.23")
```

再例如规则表输出：

```json
{
  "rate_value": "0.20"
}
```

解析后：

```go
RawValue = "0.20"
Value    = decimal.RequireFromString("0.20")
```

设计目的：

```text
RawValue 用于审计和排查；
Value 用于后续计算和类型安全访问。
```

不要让表达式引擎直接使用 RawValue。

---

## 15. FactorValue 类型安全访问方法

不要让业务代码直接写：

```go
amount := fv.Value.(decimal.Decimal)
```

推荐提供 `AsXXX` 方法。

```go
func (v FactorValue) IsOK() bool {
    return v.Status == FactorStatusOK
}

func (v FactorValue) AsDecimal() (decimal.Decimal, error) {
    if v.Status != FactorStatusOK {
        return decimal.Zero, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
    }
    if v.DataType != DataTypeDecimal {
        return decimal.Zero, fmt.Errorf("factor %s is not DECIMAL, got %s", v.Code, v.DataType)
    }
    d, ok := v.Value.(decimal.Decimal)
    if !ok {
        return decimal.Zero, fmt.Errorf("factor %s value type mismatch, expected decimal.Decimal", v.Code)
    }
    return d, nil
}

func (v FactorValue) AsString() (string, error) {
    if v.Status != FactorStatusOK {
        return "", fmt.Errorf("factor %s status is %s", v.Code, v.Status)
    }
    if v.DataType != DataTypeString {
        return "", fmt.Errorf("factor %s is not STRING, got %s", v.Code, v.DataType)
    }
    s, ok := v.Value.(string)
    if !ok {
        return "", fmt.Errorf("factor %s value type mismatch, expected string", v.Code)
    }
    return s, nil
}

func (v FactorValue) AsInt64() (int64, error) {
    if v.Status != FactorStatusOK {
        return 0, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
    }
    if v.DataType != DataTypeInt64 {
        return 0, fmt.Errorf("factor %s is not INT64, got %s", v.Code, v.DataType)
    }
    n, ok := v.Value.(int64)
    if !ok {
        return 0, fmt.Errorf("factor %s value type mismatch, expected int64", v.Code)
    }
    return n, nil
}

func (v FactorValue) AsBool() (bool, error) {
    if v.Status != FactorStatusOK {
        return false, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
    }
    if v.DataType != DataTypeBool {
        return false, fmt.Errorf("factor %s is not BOOL, got %s", v.Code, v.DataType)
    }
    b, ok := v.Value.(bool)
    if !ok {
        return false, fmt.Errorf("factor %s value type mismatch, expected bool", v.Code)
    }
    return b, nil
}

func (v FactorValue) AsTime() (time.Time, error) {
    if v.Status != FactorStatusOK {
        return time.Time{}, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
    }
    if v.DataType != DataTypeDateTime {
        return time.Time{}, fmt.Errorf("factor %s is not DATETIME, got %s", v.Code, v.DataType)
    }
    t, ok := v.Value.(time.Time)
    if !ok {
        return time.Time{}, fmt.Errorf("factor %s value type mismatch, expected time.Time", v.Code)
    }
    return t, nil
}
```

---

## 16. 复杂返回值：map / array / struct 如何处理

因子结果确实可能是复杂对象，例如：

```text
merchant_profile
order_items
promotion_detail
```

对应类型：

```text
OBJECT
ARRAY
```

但是建议：

```text
OBJECT / ARRAY 因子默认不能直接进入表达式。
```

原因：

```text
1. 表达式会和对象结构强绑定；
2. 不利于审计；
3. Govaluate 对复杂对象访问不适合作为计费规则基础；
4. 后续表达式引擎替换困难；
5. 配置人员容易写出复杂逻辑。
```

推荐做法：

```text
复杂对象可以作为中间因子；
最终参与表达式的应该是从对象中提取出来的标量因子。
```

例如：

```text
merchant_profile    RPC，返回 OBJECT
merchant_level      从 merchant_profile 中提取 STRING
merchant_country    从 merchant_profile 中提取 STRING
```

也可以让 RPC 因子直接配置 output_path：

```yaml
factor_code: merchant_level
factor_type: RPC
data_type: STRING
resolver_config:
  provider_code: GET_MERCHANT_INFO
  output_path: "$.merchant.level"
```

这样最终进入表达式的是：

```text
merchant_level = "VIP"
```

而不是：

```text
merchant_profile.level
```

---

## 17. ToExpressionParam 设计

进入表达式引擎前，需要把 `FactorValue` 转成表达式参数。

但是不是所有因子都可以进入表达式。

推荐允许：

```text
DECIMAL
INT64
STRING
BOOL
```

默认不允许：

```text
DATETIME
OBJECT
ARRAY
```

实现：

```go
func (v FactorValue) ToExpressionParam() (any, error) {
    if v.Status != FactorStatusOK {
        return nil, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
    }

    switch v.DataType {
    case DataTypeDecimal:
        // 注意：这里不要转 float64。
        // Govaluate 需要配合 decimal 函数或表达式引擎适配层。
        return v.AsDecimal()

    case DataTypeInt64:
        return v.AsInt64()

    case DataTypeString:
        return v.AsString()

    case DataTypeBool:
        return v.AsBool()

    case DataTypeDateTime:
        return nil, fmt.Errorf("DATETIME factor %s is not allowed in expression", v.Code)

    case DataTypeObject, DataTypeArray:
        return nil, fmt.Errorf("complex factor %s is not allowed in expression", v.Code)

    default:
        return nil, fmt.Errorf("unsupported factor data type: %s", v.DataType)
    }
}
```

构建表达式参数：

```go
func BuildExpressionParams(
    values map[FactorCode]FactorValue,
    required []FactorCode,
) (map[string]interface{}, error) {
    params := make(map[string]interface{}, len(required))

    for _, code := range required {
        fv, ok := values[code]
        if !ok {
            return nil, fmt.Errorf("factor %s not resolved", code)
        }

        param, err := fv.ToExpressionParam()
        if err != nil {
            return nil, err
        }

        params[string(code)] = param
    }

    return params, nil
}
```

关键原则：

```text
不要把所有因子都传给 Govaluate；
只传当前表达式真正依赖的因子；
只传允许进入表达式的数据类型。
```

---

## 18. Snapshot 设计

因子结果需要落库做审计。

不能直接 `json.Marshal(FactorValue.Value)`，因为：

```text
1. decimal.Decimal 需要转 string；
2. time.Time 需要统一格式；
3. RawValue 和 Value 语义不同；
4. 错误状态也需要保存；
5. 来源信息需要结构化保存。
```

推荐：

```go
type FactorValueSnapshot struct {
    Code         FactorCode     `json:"code"`
    DataType     FactorDataType `json:"data_type"`
    Status       FactorStatus   `json:"status"`
    Value        any            `json:"value,omitempty"`
    RawValue     any            `json:"raw_value,omitempty"`
    Source       FactorSource   `json:"source"`
    ErrorCode    string         `json:"error_code,omitempty"`
    ErrorMessage string         `json:"error_message,omitempty"`
    Extra        map[string]any `json:"extra,omitempty"`
}
```

`SnapshotValue`：

```go
func (v FactorValue) SnapshotValue() any {
    if v.Status != FactorStatusOK {
        return nil
    }

    switch v.DataType {
    case DataTypeDecimal:
        d, ok := v.Value.(decimal.Decimal)
        if !ok {
            return nil
        }
        return d.String()

    case DataTypeDateTime:
        t, ok := v.Value.(time.Time)
        if !ok {
            return nil
        }
        return t.Format(time.RFC3339Nano)

    case DataTypeString, DataTypeInt64, DataTypeBool:
        return v.Value

    case DataTypeObject, DataTypeArray:
        return v.Value

    default:
        return fmt.Sprintf("%v", v.Value)
    }
}
```

`Snapshot`：

```go
func (v FactorValue) Snapshot() FactorValueSnapshot {
    return FactorValueSnapshot{
        Code:         v.Code,
        DataType:     v.DataType,
        Status:       v.Status,
        Value:        v.SnapshotValue(),
        RawValue:     v.RawValue,
        Source:       v.Source,
        ErrorCode:    v.ErrorCode,
        ErrorMessage: v.ErrorMessage,
        Extra:        v.Extra,
    }
}
```

---

## 19. 构造函数设计

不要在 Resolver 中到处手写 `FactorValue`。

推荐提供 helper。

### 19.1 成功值

```go
func NewOKFactorValue(
    code FactorCode,
    dataType FactorDataType,
    value any,
    rawValue any,
    source FactorSource,
) FactorValue {
    return FactorValue{
        Code:     code,
        DataType: dataType,
        Status:   FactorStatusOK,
        Value:    value,
        RawValue: rawValue,
        Source:   source,
    }
}
```

---

### 19.2 Missing

```go
func NewMissingFactorValue(
    code FactorCode,
    dataType FactorDataType,
    source FactorSource,
) FactorValue {
    return FactorValue{
        Code:         code,
        DataType:     dataType,
        Status:       FactorStatusMissing,
        Source:       source,
        ErrorCode:    "FACTOR_MISSING",
        ErrorMessage: "factor value is missing",
    }
}
```

---

### 19.3 Failed

```go
func NewFailedFactorValue(
    code FactorCode,
    dataType FactorDataType,
    source FactorSource,
    errorCode string,
    errorMessage string,
) FactorValue {
    return FactorValue{
        Code:         code,
        DataType:     dataType,
        Status:       FactorStatusFailed,
        Source:       source,
        ErrorCode:    errorCode,
        ErrorMessage: errorMessage,
    }
}
```

---

### 19.4 Invalid

```go
func NewInvalidFactorValue(
    code FactorCode,
    dataType FactorDataType,
    rawValue any,
    source FactorSource,
    errorMessage string,
) FactorValue {
    return FactorValue{
        Code:         code,
        DataType:     dataType,
        Status:       FactorStatusInvalid,
        RawValue:     rawValue,
        Source:       source,
        ErrorCode:    "FACTOR_INVALID",
        ErrorMessage: errorMessage,
    }
}
```

---

## 20. NormalizeRawValue 设计

所有从外部来的值都应该归一化。

```go
func NormalizeRawValue(raw any, dataType FactorDataType) (any, error) {
    switch dataType {
    case DataTypeDecimal:
        return normalizeDecimal(raw)
    case DataTypeString:
        return normalizeString(raw)
    case DataTypeInt64:
        return normalizeInt64(raw)
    case DataTypeBool:
        return normalizeBool(raw)
    case DataTypeDateTime:
        return normalizeTime(raw)
    case DataTypeObject:
        return normalizeObject(raw)
    case DataTypeArray:
        return normalizeArray(raw)
    default:
        return nil, fmt.Errorf("unsupported data type: %s", dataType)
    }
}
```

Decimal 归一化示例：

```go
func normalizeDecimal(raw any) (decimal.Decimal, error) {
    switch v := raw.(type) {
    case decimal.Decimal:
        return v, nil
    case string:
        return decimal.NewFromString(v)
    case int64:
        return decimal.NewFromInt(v), nil
    case int:
        return decimal.NewFromInt(int64(v)), nil
    case json.Number:
        return decimal.NewFromString(v.String())
    default:
        return decimal.Zero, fmt.Errorf("cannot convert %T to decimal", raw)
    }
}
```

注意：

```text
不建议直接接受 float64 作为金额来源。
```

如果必须兼容 float64，需要明确记录风险，或者只允许非金额类 decimal 使用。

---

## 21. Resolver 实现示例：EVENT_FIELD

```go
type EventFieldConfig struct {
    SourcePath string `json:"source_path"`
}

type EventFieldResolver struct{}

func (r *EventFieldResolver) Type() FactorType {
    return FactorTypeEventField
}

func (r *EventFieldResolver) Validate(def FactorDefinition, catalog FactorCatalog) error {
    var cfg EventFieldConfig
    if err := json.Unmarshal(def.ResolverConfig, &cfg); err != nil {
        return err
    }
    if cfg.SourcePath == "" {
        return fmt.Errorf("source_path is required")
    }
    return nil
}

func (r *EventFieldResolver) Dependencies(def FactorDefinition) ([]FactorCode, error) {
    return nil, nil
}

func (r *EventFieldResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
    var cfg EventFieldConfig
    if err := json.Unmarshal(req.Factor.ResolverConfig, &cfg); err != nil {
        return NewFailedFactorValue(
            req.Factor.Code,
            req.Factor.DataType,
            FactorSource{FactorType: FactorTypeEventField},
            "CONFIG_ERROR",
            err.Error(),
        ), nil
    }

    source := FactorSource{
        FactorType: FactorTypeEventField,
        SourcePath: cfg.SourcePath,
    }

    raw, found := req.Event.GetByPath(cfg.SourcePath)
    if !found {
        if req.Factor.DefaultValue != nil {
            value, err := NormalizeRawValue(*req.Factor.DefaultValue, req.Factor.DataType)
            if err != nil {
                return NewInvalidFactorValue(
                    req.Factor.Code,
                    req.Factor.DataType,
                    *req.Factor.DefaultValue,
                    source,
                    err.Error(),
                ), nil
            }
            return NewOKFactorValue(req.Factor.Code, req.Factor.DataType, value, *req.Factor.DefaultValue, source), nil
        }
        return NewMissingFactorValue(req.Factor.Code, req.Factor.DataType, source), nil
    }

    value, err := NormalizeRawValue(raw, req.Factor.DataType)
    if err != nil {
        return NewInvalidFactorValue(req.Factor.Code, req.Factor.DataType, raw, source, err.Error()), nil
    }

    return NewOKFactorValue(req.Factor.Code, req.Factor.DataType, value, raw, source), nil
}
```

---

## 22. Resolver 实现示例：RULE_TABLE

```go
type RuleTableConfig struct {
    RuleTableCode string            `json:"rule_table_code"`
    OutputColumn  string            `json:"output_column"`
    InputMapping  map[string]string `json:"input_mapping"`
}

type RuleTableResolver struct {
    RuleTableRepo RuleTableRepository
}

func (r *RuleTableResolver) Type() FactorType {
    return FactorTypeRuleTable
}

func (r *RuleTableResolver) Dependencies(def FactorDefinition) ([]FactorCode, error) {
    var cfg RuleTableConfig
    if err := json.Unmarshal(def.ResolverConfig, &cfg); err != nil {
        return nil, err
    }

    deps := make([]FactorCode, 0, len(cfg.InputMapping))
    for _, factorCode := range cfg.InputMapping {
        deps = append(deps, FactorCode(factorCode))
    }
    return deps, nil
}

func (r *RuleTableResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
    var cfg RuleTableConfig
    if err := json.Unmarshal(req.Factor.ResolverConfig, &cfg); err != nil {
        return NewFailedFactorValue(
            req.Factor.Code,
            req.Factor.DataType,
            FactorSource{FactorType: FactorTypeRuleTable},
            "CONFIG_ERROR",
            err.Error(),
        ), nil
    }

    inputs := make(map[string]any)
    for inputName, factorCode := range cfg.InputMapping {
        fv, ok := req.Values.Get(FactorCode(factorCode))
        if !ok || fv.Status != FactorStatusOK {
            return NewFailedFactorValue(
                req.Factor.Code,
                req.Factor.DataType,
                FactorSource{FactorType: FactorTypeRuleTable, RuleTableCode: cfg.RuleTableCode},
                "DEPENDENCY_NOT_READY",
                factorCode,
            ), nil
        }
        inputs[inputName] = fv.Value
    }

    matched, err := r.RuleTableRepo.Match(ctx, RuleTableMatchRequest{
        RuleTableCode: cfg.RuleTableCode,
        Inputs:        inputs,
        OutputColumn:  cfg.OutputColumn,
    })
    if err != nil {
        return FactorValue{}, err
    }

    source := FactorSource{
        FactorType:    FactorTypeRuleTable,
        RuleTableCode: cfg.RuleTableCode,
        MatchedInputs: inputs,
    }

    if !matched.Found {
        return NewFailedFactorValue(
            req.Factor.Code,
            req.Factor.DataType,
            source,
            "RULE_TABLE_NO_MATCH",
            "no matched rule row",
        ), nil
    }

    source.MatchedRowID = matched.RowID
    source.Version = matched.Version
    source.CostMs = matched.CostMs

    value, err := NormalizeRawValue(matched.OutputValue, req.Factor.DataType)
    if err != nil {
        return NewInvalidFactorValue(req.Factor.Code, req.Factor.DataType, matched.OutputValue, source, err.Error()), nil
    }

    return NewOKFactorValue(req.Factor.Code, req.Factor.DataType, value, matched.OutputValue, source), nil
}
```

---

## 23. Resolver 实现示例：RPC 泛化调用

### 23.1 泛化 RPC 返回值特点

普通强类型 RPC：

```go
resp, err := merchantClient.GetMerchantLevel(ctx, req)
// resp 是 *GetMerchantLevelResponse
```

泛化 RPC：

```go
resp, err := genericInvoker.Invoke(ctx, method, params)
// resp 通常是 any
```

泛化调用返回值可能是：

```text
map[string]any
[]any
[]byte
string(JSON)
proto.Message
dynamicpb.Message
框架自定义 GenericResponse
```

因此 RPC Resolver 不能直接把泛化返回值传下去，必须做：

```text
raw response
  -> output_path 提取
  -> data_type 归一化
  -> FactorValue
```

---

### 23.2 RPC Provider 接口

```go
type RPCProvider interface {
    Code() string
    Call(ctx context.Context, req RPCCallRequest) (RPCCallResponse, error)
}

type RPCCallRequest struct {
    Method string
    Params map[string]any
}

type RPCCallResponse struct {
    Body      any
    Raw       any
    Headers   map[string]string
    CostMs    int64
    TraceID   string
}
```

---

### 23.3 RPC Resolver 配置

```go
type RPCConfig struct {
    ProviderCode string            `json:"provider_code"`
    Method       string            `json:"method"`
    InputMapping map[string]string `json:"input_mapping"`
    OutputPath   string            `json:"output_path"`
    TimeoutMs    int64             `json:"timeout_ms"`
}
```

示例：

```json
{
  "provider_code": "GET_MERCHANT_LEVEL",
  "method": "MerchantService.GetMerchantLevel",
  "input_mapping": {
    "merchant_id": "merchant_id",
    "as_of_time": "payment_success_time"
  },
  "output_path": "$.data.level",
  "timeout_ms": 300
}
```

---

### 23.4 RPC Resolver 实现骨架

```go
type RPCResolver struct {
    Providers RPCProviderRegistry
}

func (r *RPCResolver) Type() FactorType {
    return FactorTypeRPC
}

func (r *RPCResolver) Dependencies(def FactorDefinition) ([]FactorCode, error) {
    var cfg RPCConfig
    if err := json.Unmarshal(def.ResolverConfig, &cfg); err != nil {
        return nil, err
    }

    deps := make([]FactorCode, 0, len(cfg.InputMapping))
    for _, factorCode := range cfg.InputMapping {
        deps = append(deps, FactorCode(factorCode))
    }
    return deps, nil
}

func (r *RPCResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
    var cfg RPCConfig
    if err := json.Unmarshal(req.Factor.ResolverConfig, &cfg); err != nil {
        return NewFailedFactorValue(
            req.Factor.Code,
            req.Factor.DataType,
            FactorSource{FactorType: FactorTypeRPC},
            "CONFIG_ERROR",
            err.Error(),
        ), nil
    }

    provider, ok := r.Providers.Get(cfg.ProviderCode)
    if !ok {
        return NewFailedFactorValue(
            req.Factor.Code,
            req.Factor.DataType,
            FactorSource{FactorType: FactorTypeRPC, ProviderCode: cfg.ProviderCode},
            "PROVIDER_NOT_FOUND",
            cfg.ProviderCode,
        ), nil
    }

    rpcReq := make(map[string]any)
    for argName, factorCode := range cfg.InputMapping {
        fv, ok := req.Values.Get(FactorCode(factorCode))
        if !ok || fv.Status != FactorStatusOK {
            return NewFailedFactorValue(
                req.Factor.Code,
                req.Factor.DataType,
                FactorSource{FactorType: FactorTypeRPC, ProviderCode: cfg.ProviderCode},
                "DEPENDENCY_NOT_READY",
                factorCode,
            ), nil
        }
        rpcReq[argName] = fv.Value
    }

    resp, err := provider.Call(ctx, RPCCallRequest{
        Method: cfg.Method,
        Params: rpcReq,
    })
    if err != nil {
        return FactorValue{}, err
    }

    raw, err := ExtractByPath(resp.Body, cfg.OutputPath)
    if err != nil {
        return NewFailedFactorValue(
            req.Factor.Code,
            req.Factor.DataType,
            FactorSource{
                FactorType:   FactorTypeRPC,
                ProviderCode: cfg.ProviderCode,
                Method:       cfg.Method,
                Request:      rpcReq,
                ResponsePath: cfg.OutputPath,
                CostMs:       resp.CostMs,
            },
            "OUTPUT_PATH_ERROR",
            err.Error(),
        ), nil
    }

    value, err := NormalizeRawValue(raw, req.Factor.DataType)
    source := FactorSource{
        FactorType:   FactorTypeRPC,
        ProviderCode: cfg.ProviderCode,
        Method:       cfg.Method,
        Request:      rpcReq,
        ResponsePath: cfg.OutputPath,
        CostMs:       resp.CostMs,
    }

    if err != nil {
        return NewInvalidFactorValue(req.Factor.Code, req.Factor.DataType, raw, source, err.Error()), nil
    }

    return NewOKFactorValue(req.Factor.Code, req.Factor.DataType, value, raw, source), nil
}
```

---

## 24. 系统错误与业务解析失败的区分

Resolver 返回：

```go
(FactorValue, error)
```

这里有两个错误通道：

### 24.1 Go error：系统级错误

例如：

```text
数据库连接失败
RPC 网络错误
context timeout
panic recover 后的系统异常
```

这类错误通常代表本次计算可能需要重试。

---

### 24.2 FactorValue.Status：业务级解析结果

例如：

```text
字段缺失
RULE_TABLE 无命中
类型转换失败
RPC 返回业务不存在
output_path 不存在
```

这类错误应通过：

```go
FactorValue{
    Status: FactorStatusFailed,
    ErrorCode: "RULE_TABLE_NO_MATCH",
}
```

来表达。

推荐约定：

```text
error 表示系统级异常；
FactorStatus 表示业务级解析结果。
```

这样上层可以区分：

```text
1. 是否重试；
2. 是否进入人工处理；
3. 是否等待配置修复后重放；
4. 是否直接失败。
```

---

## 25. Resolver Registry 设计

```go
type ResolverRegistry struct {
    resolvers map[FactorType]FactorResolver
}

func NewResolverRegistry(resolvers ...FactorResolver) *ResolverRegistry {
    m := make(map[FactorType]FactorResolver)
    for _, r := range resolvers {
        m[r.Type()] = r
    }
    return &ResolverRegistry{resolvers: m}
}

func (r *ResolverRegistry) Get(t FactorType) (FactorResolver, bool) {
    resolver, ok := r.resolvers[t]
    return resolver, ok
}
```

初始化：

```go
registry := NewResolverRegistry(
    &EventFieldResolver{},
    &RuleTableResolver{RuleTableRepo: ruleTableRepo},
    &TableLookupResolver{TableRepo: tableRepo},
    &RPCResolver{Providers: rpcProviders},
    &ExpressionResolver{Engine: expressionEngine},
    &ConstantResolver{},
)
```

---

## 26. FactorExecutor 设计

Resolver 只负责单个因子的解析。

还需要 FactorExecutor 负责统一调度：

```text
1. 构建因子依赖 DAG；
2. 检查循环依赖；
3. 按拓扑顺序解析；
4. 同一事件内因子去重；
5. 可并行因子并发解析；
6. 处理 required 因子失败；
7. 生成完整因子上下文。
```

示例：

```go
type FactorExecutor struct {
    Catalog  FactorCatalog
    Registry *ResolverRegistry
}

func (e *FactorExecutor) ResolveFactors(
    ctx context.Context,
    event NormalizedEvent,
    required []FactorCode,
    ruleCtx RuleContext,
) (map[FactorCode]FactorValue, error) {
    dag, err := e.BuildDAG(required)
    if err != nil {
        return nil, err
    }

    values := NewInMemoryFactorValueStore()

    for _, code := range dag.TopologicalOrder() {
        def, err := e.Catalog.Get(code)
        if err != nil {
            return nil, err
        }

        resolver, ok := e.Registry.Get(def.Type)
        if !ok {
            return nil, fmt.Errorf("resolver not found for factor type %s", def.Type)
        }

        fv, err := resolver.Resolve(ctx, ResolveRequest{
            Event:   event,
            Factor:  def,
            Values:  values,
            RuleCtx: ruleCtx,
        })
        if err != nil {
            return nil, err
        }

        values.Set(code, fv)

        if fv.Status != FactorStatusOK && def.Required {
            return nil, fmt.Errorf("required factor %s failed: %s", code, fv.ErrorCode)
        }
    }

    return values.All(), nil
}
```

---

## 27. interface 的边界

完全不用 `interface{}` 在 Go 中并不现实，因为因子结果确实可能是：

```text
string
int64
decimal.Decimal
bool
time.Time
map[string]any
[]any
```

但需要明确边界。

### 27.1 Safe Zone

```text
FactorDefinition
ResolveRequest
FactorValue
FactorValueStore
CalculationContext
FeeRule
Snapshot
```

### 27.2 Unsafe Zone

```text
JSON RawMessage
RPC 原始 response
map[string]any
Govaluate params
RawValue
```

设计原则：

```text
Unsafe Zone 的数据进入 Safe Zone 时，必须做类型转换、状态标记、source 记录和错误码记录。
```

最终原则：

```text
interface 可以出现在 Value / RawValue / Extra / 适配层；
interface 不应该出现在 Resolver 主输入输出接口上。
```

---

## 28. 如果已有 Fetch(ctx, interface{}) 如何兼容

如果系统已有老接口：

```go
type RawFetcher interface {
    Fetch(ctx context.Context, input any) (any, error)
}
```

可以做一层 Adapter：

```go
type RawFetcherAdapter struct {
    Fetcher RawFetcher
}

func (a *RawFetcherAdapter) Type() FactorType {
    return FactorTypeRPC
}

func (a *RawFetcherAdapter) Validate(def FactorDefinition, catalog FactorCatalog) error {
    return nil
}

func (a *RawFetcherAdapter) Dependencies(def FactorDefinition) ([]FactorCode, error) {
    // 从配置中解析依赖
    return nil, nil
}

func (a *RawFetcherAdapter) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
    input, err := BuildTypedInput(req)
    if err != nil {
        return NewFailedFactorValue(
            req.Factor.Code,
            req.Factor.DataType,
            FactorSource{FactorType: req.Factor.Type},
            "BUILD_INPUT_ERROR",
            err.Error(),
        ), nil
    }

    raw, err := a.Fetcher.Fetch(ctx, input)
    if err != nil {
        return FactorValue{}, err
    }

    value, err := NormalizeRawValue(raw, req.Factor.DataType)
    if err != nil {
        return NewInvalidFactorValue(
            req.Factor.Code,
            req.Factor.DataType,
            raw,
            FactorSource{FactorType: req.Factor.Type},
            err.Error(),
        ), nil
    }

    return NewOKFactorValue(
        req.Factor.Code,
        req.Factor.DataType,
        value,
        raw,
        FactorSource{FactorType: req.Factor.Type},
    ), nil
}
```

这样可以逐步迁移老实现，但新系统对外仍保持 `ResolveRequest -> FactorValue`。

---

## 29. 推荐代码目录结构

```text
billing/
  factor/
    definition.go
    value.go
    source.go
    resolver.go
    executor.go
    registry.go
    store.go
    dag.go
    normalize.go

    resolver_event_field.go
    resolver_rpc.go
    resolver_table_lookup.go
    resolver_rule_table.go
    resolver_expression.go
    resolver_constant.go

  expression/
    engine.go
    govaluate_adapter.go
    decimal_functions.go

  rule/
    fee_rule.go
    rule_table.go
    matcher.go

  event/
    normalized_event.go
    parser.go

  posting/
    fee_item.go
    repository.go
```

---

## 30. 最小可落地版本

第一版建议至少实现：

```text
1. FactorDefinition；
2. FactorType / FactorDataType；
3. FactorResolver 接口；
4. ResolveRequest；
5. FactorValue；
6. FactorValueStore；
7. EventFieldResolver；
8. RuleTableResolver；
9. RPCResolver 框架；
10. NormalizeRawValue；
11. ToExpressionParam；
12. Snapshot；
13. ResolverRegistry；
14. FactorExecutor 顺序版 DAG。
```

可以暂缓：

```text
1. 并发 DAG 执行；
2. 复杂 TypedValue interface；
3. 完整 Object/Array 字段提取；
4. RPC provider 高级治理；
5. 规则表内存执行引擎。
```

---

## 31. 最终推荐版本

### 31.1 Resolver 接口

```go
type FactorResolver interface {
    Type() FactorType
    Validate(def FactorDefinition, catalog FactorCatalog) error
    Dependencies(def FactorDefinition) ([]FactorCode, error)
    Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error)
}
```

### 31.2 FactorValue 核心结构

```go
type FactorValue struct {
    Code     FactorCode
    DataType FactorDataType
    Status   FactorStatus

    Value    any
    RawValue any

    Source FactorSource

    ErrorCode    string
    ErrorMessage string
    Extra        map[string]any
}
```

### 31.3 访问方式

```go
fv.AsDecimal()
fv.AsString()
fv.AsInt64()
fv.AsBool()
fv.AsTime()
fv.ToExpressionParam()
fv.Snapshot()
```

---

## 32. 一句话总结

```text
不要让每个业务因子都实现 Fetch(ctx, interface{}) (interface{}, error)。

更合理的做法是：
FactorDefinition 负责配置，FactorResolver 负责按取数类型执行，ResolveRequest 作为强类型输入，FactorValue 作为统一输出。

interface 可以存在，但必须被限制在 FactorValue.Value、RawValue 和少数适配层里，不能扩散到整个因子系统的主接口。
```

最终目标是：

```text
既保留配置化计费系统需要的灵活性，
又不牺牲金融系统需要的类型安全、可审计性和可重算能力。
```

