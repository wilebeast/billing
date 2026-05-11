package factor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type FactorCode string

type FactorType string

const (
	FactorTypeEventField  FactorType = "EVENT_FIELD"
	FactorTypeExpression  FactorType = "EXPRESSION"
	FactorTypeRPC         FactorType = "RPC"
	FactorTypeTableLookup FactorType = "TABLE_LOOKUP"
	FactorTypeRuleTable   FactorType = "RULE_TABLE"
	FactorTypeRateMatch   FactorType = "RATE_MATCH"
	FactorTypeConstant    FactorType = "CONSTANT"

	// Backward-compatible alias.
	FactorTypeTable FactorType = FactorTypeTableLookup
)

type FactorScope string

const (
	FactorScopeOrder  FactorScope = "ORDER"
	FactorScopeItem   FactorScope = "ITEM"
	FactorScopeGlobal FactorScope = "GLOBAL"
)

type FactorDataType string

const (
	FactorDataTypeDecimal  FactorDataType = "DECIMAL"
	FactorDataTypeString   FactorDataType = "STRING"
	FactorDataTypeInt64    FactorDataType = "INT64"
	FactorDataTypeBool     FactorDataType = "BOOL"
	FactorDataTypeDatetime FactorDataType = "DATETIME"
	FactorDataTypeObject   FactorDataType = "OBJECT"
	FactorDataTypeArray    FactorDataType = "ARRAY"

	// Backward-compatible aliases.
	FactorDataTypeInt    FactorDataType = FactorDataTypeInt64
	FactorDataTypeStruct FactorDataType = FactorDataTypeObject
	FactorDataTypeList   FactorDataType = FactorDataTypeArray
	FactorDataTypeMap    FactorDataType = FactorDataTypeObject
)

type FactorStatus string

const (
	FactorStatusOK        FactorStatus = "OK"
	FactorStatusNull      FactorStatus = "NULL"
	FactorStatusMissing   FactorStatus = "MISSING"
	FactorStatusDefaulted FactorStatus = "DEFAULTED"
	FactorStatusInvalid   FactorStatus = "INVALID"
	FactorStatusFailed    FactorStatus = "FAILED"

	// Backward-compatible alias.
	FactorStatusError FactorStatus = FactorStatusFailed
)

type EventFieldConfig struct {
	SourcePath string `json:"source_path"`
}

type ExpressionConfig struct {
	Expression    string   `json:"expression"`
	DependFactors []string `json:"depend_factors,omitempty"`
}

type RPCConfig struct {
	ProviderCode string            `json:"provider_code"`
	Method       string            `json:"method"`
	InputMapping map[string]string `json:"input_mapping,omitempty"`
	OutputPath   string            `json:"output_path,omitempty"`
}

type TableLookupConfig struct {
	TableCode    string            `json:"table_code"`
	LookupKey    map[string]string `json:"lookup_key,omitempty"`
	OutputPath   string            `json:"output_path,omitempty"`
	DefaultFound bool              `json:"default_found,omitempty"`
}

type RuleTableConfig struct {
	RuleTableCode    string            `json:"rule_table_code"`
	InputMapping     map[string]string `json:"input_mapping,omitempty"`
	OutputPath       string            `json:"output_path,omitempty"`
	NoMatchStrategy  string            `json:"no_match_strategy,omitempty"`
	ConflictStrategy string            `json:"conflict_strategy,omitempty"`
}

type ConstantConfig struct {
	Value any `json:"value"`
}

// FactorDefinition is the persisted/platform definition of a factor.
// Resolver-specific config can either live in dedicated typed fields or in ResolverConfig.
type FactorDefinition struct {
	Code           FactorCode      `json:"code"`
	Name           string          `json:"name,omitempty"`
	Type           FactorType      `json:"type"`
	Scope          FactorScope     `json:"scope,omitempty"`
	DataType       FactorDataType  `json:"data_type"`
	Required       bool            `json:"required"`
	Category       string          `json:"category,omitempty"`
	Version        int64           `json:"version,omitempty"`
	DefaultValue   *string         `json:"default_value,omitempty"`
	ResolverConfig json.RawMessage `json:"resolver_config,omitempty"`

	// Backward-compatible typed config fields for in-memory construction.
	EventField *EventFieldConfig  `json:"-"`
	Expression *ExpressionConfig  `json:"-"`
	RPC        *RPCConfig         `json:"-"`
	Table      *TableLookupConfig `json:"-"`
	RuleTable  *RuleTableConfig   `json:"-"`
	Constant   *ConstantConfig    `json:"-"`
}

func (d FactorDefinition) eventFieldConfig() (*EventFieldConfig, error) {
	if d.EventField != nil {
		return d.EventField, nil
	}
	if len(d.ResolverConfig) == 0 {
		return nil, nil
	}
	var cfg EventFieldConfig
	if err := json.Unmarshal(d.ResolverConfig, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (d FactorDefinition) expressionConfig() (*ExpressionConfig, error) {
	if d.Expression != nil {
		return d.Expression, nil
	}
	if len(d.ResolverConfig) == 0 {
		return nil, nil
	}
	var cfg ExpressionConfig
	if err := json.Unmarshal(d.ResolverConfig, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (d FactorDefinition) rpcConfig() (*RPCConfig, error) {
	if d.RPC != nil {
		return d.RPC, nil
	}
	if len(d.ResolverConfig) == 0 {
		return nil, nil
	}
	var cfg RPCConfig
	if err := json.Unmarshal(d.ResolverConfig, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (d FactorDefinition) tableLookupConfig() (*TableLookupConfig, error) {
	if d.Table != nil {
		return d.Table, nil
	}
	if len(d.ResolverConfig) == 0 {
		return nil, nil
	}
	var cfg TableLookupConfig
	if err := json.Unmarshal(d.ResolverConfig, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (d FactorDefinition) ruleTableConfig() (*RuleTableConfig, error) {
	if d.RuleTable != nil {
		return d.RuleTable, nil
	}
	if len(d.ResolverConfig) == 0 {
		return nil, nil
	}
	var cfg RuleTableConfig
	if err := json.Unmarshal(d.ResolverConfig, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (d FactorDefinition) constantConfig() (*ConstantConfig, error) {
	if d.Constant != nil {
		return d.Constant, nil
	}
	if len(d.ResolverConfig) == 0 {
		return nil, nil
	}
	var cfg ConstantConfig
	if err := json.Unmarshal(d.ResolverConfig, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (d FactorDefinition) ValidateMVP() error {
	switch d.Type {
	case FactorTypeEventField:
		cfg, err := d.eventFieldConfig()
		if err != nil {
			return fmt.Errorf("factor %s: decode event field config: %w", d.Code, err)
		}
		if cfg == nil || cfg.SourcePath == "" {
			return fmt.Errorf("factor %s: missing event field config", d.Code)
		}
	case FactorTypeExpression:
		cfg, err := d.expressionConfig()
		if err != nil {
			return fmt.Errorf("factor %s: decode expression config: %w", d.Code, err)
		}
		if cfg == nil || cfg.Expression == "" {
			return fmt.Errorf("factor %s: missing expression config", d.Code)
		}
	case FactorTypeRPC:
		cfg, err := d.rpcConfig()
		if err != nil {
			return fmt.Errorf("factor %s: decode rpc config: %w", d.Code, err)
		}
		if cfg == nil || cfg.ProviderCode == "" || cfg.Method == "" {
			return fmt.Errorf("factor %s: missing rpc config", d.Code)
		}
	case FactorTypeTableLookup:
		cfg, err := d.tableLookupConfig()
		if err != nil {
			return fmt.Errorf("factor %s: decode table config: %w", d.Code, err)
		}
		if cfg == nil || cfg.TableCode == "" || len(cfg.LookupKey) == 0 {
			return fmt.Errorf("factor %s: missing table config", d.Code)
		}
	case FactorTypeRuleTable, FactorTypeRateMatch:
		cfg, err := d.ruleTableConfig()
		if err != nil {
			return fmt.Errorf("factor %s: decode rule table config: %w", d.Code, err)
		}
		if cfg == nil || cfg.RuleTableCode == "" || len(cfg.InputMapping) == 0 {
			return fmt.Errorf("factor %s: missing rule table config", d.Code)
		}
	case FactorTypeConstant:
		cfg, err := d.constantConfig()
		if err != nil {
			return fmt.Errorf("factor %s: decode constant config: %w", d.Code, err)
		}
		if cfg == nil {
			return fmt.Errorf("factor %s: missing constant config", d.Code)
		}
	default:
		return fmt.Errorf("factor %s: unsupported factor type %s", d.Code, d.Type)
	}
	return nil
}

type TypedFactorContext struct {
	Factors map[string]FactorValue
}

type RuleContext struct {
	FeeCode     string
	RuleID      string
	RuleVersion int64
	BizScene    string
}

type CalculationMeta struct {
	EventID        string
	MerchantID     string
	BizOrderID     string
	BizTime        time.Time
	TraceID        string
	ChargeScope    FactorScope
	ChargeObjectID string
}

type NormalizedEvent interface {
	GetByPath(path string) (any, bool, error)
	EventID() string
	EventType() string
	BizTime() time.Time
}

type ScopedValueGetter interface {
	GetByScope(scope FactorScope, path string) (any, bool, error)
}

type MapEvent struct {
	ID      string
	Type    string
	Time    time.Time
	Payload map[string]any
}

func NewMapEvent(payload map[string]any) MapEvent {
	return MapEvent{Payload: payload}
}

func (e MapEvent) GetByPath(path string) (any, bool, error) {
	return getByPath(e.Payload, path)
}

func (e MapEvent) EventID() string    { return e.ID }
func (e MapEvent) EventType() string  { return e.Type }
func (e MapEvent) BizTime() time.Time { return e.Time }

type ChargeObject struct {
	Scope   FactorScope
	ID      string
	Payload map[string]any
}

type CalculationContext struct {
	Event        MapEvent
	ChargeObject ChargeObject
}

type FactorInstanceKey struct {
	Code           FactorCode
	Scope          FactorScope
	ChargeObjectID string
}

func NewCalculationContext(event map[string]any, object ChargeObject) CalculationContext {
	return CalculationContext{
		Event:        NewMapEvent(event),
		ChargeObject: object,
	}
}

func (c CalculationContext) GetByPath(path string) (any, bool, error) {
	return c.Event.GetByPath(path)
}

func (c CalculationContext) EventID() string    { return c.Event.EventID() }
func (c CalculationContext) EventType() string  { return c.Event.EventType() }
func (c CalculationContext) BizTime() time.Time { return c.Event.BizTime() }

func (c CalculationContext) GetByScope(scope FactorScope, path string) (any, bool, error) {
	switch scope {
	case FactorScopeItem:
		if c.ChargeObject.Payload == nil {
			return nil, false, nil
		}
		return getByPath(c.ChargeObject.Payload, path)
	case FactorScopeOrder, FactorScopeGlobal, "":
		return c.Event.GetByPath(path)
	default:
		return c.Event.GetByPath(path)
	}
}

func (c CalculationContext) FactorInstanceKey(def FactorDefinition) FactorInstanceKey {
	key := FactorInstanceKey{
		Code:  def.Code,
		Scope: def.Scope,
	}
	if def.Scope == FactorScopeItem {
		key.ChargeObjectID = c.ChargeObject.ID
	}
	return key
}

type FactorCatalog map[FactorCode]FactorDefinition

func (c FactorCatalog) Get(code FactorCode) (FactorDefinition, bool) {
	def, ok := c[code]
	return def, ok
}

func (c FactorCatalog) Has(code string) bool {
	_, ok := c[FactorCode(code)]
	return ok
}

type ResolveContext struct {
	Event             NormalizedEvent
	ResolveDependency func(ctx context.Context, code FactorCode) (FactorValue, error)
	RPCProvider       RPCProvider
	TableProvider     TableProvider
	RuleProvider      RuleTableRepository
}

type InputBinding struct {
	SourcePath string
	Dependency FactorCode
}

type Factor interface {
	Definition() FactorDefinition
	Resolve(ctx context.Context, req ResolveContext) (FactorValue, error)
}

type FactorProvider interface {
	Type() FactorType
	NewFactor(def FactorDefinition, catalog FactorCatalog) (Factor, error)
}
