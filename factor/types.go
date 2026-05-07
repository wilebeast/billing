package factor

import (
	"context"
	"fmt"
)

type FactorType string

const (
	FactorTypeEventField FactorType = "EVENT_FIELD"
	FactorTypeExpression FactorType = "EXPRESSION"
	FactorTypeRPC        FactorType = "RPC"
	FactorTypeTable      FactorType = "TABLE_LOOKUP"
)

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
	FactorType FactorType
	DataType   FactorDataType
	Status     FactorStatus
	Value      any
	ValueText  string
}

type EventFieldConfig struct {
	SourcePath string
}

type ExpressionConfig struct {
	Expression    string
	DependFactors []string
}

type RPCConfig struct {
	ProviderCode string
	Method       string
	InputMapping map[string]string
	OutputPath   string
}

type TableLookupConfig struct {
	TableCode    string
	LookupKey    map[string]string
	OutputPath   string
	DefaultFound bool
}

type FactorDefinition struct {
	Code         string
	Name         string
	Type         FactorType
	DataType     FactorDataType
	Required     bool
	DefaultValue *string

	EventField *EventFieldConfig
	Expression *ExpressionConfig
	RPC        *RPCConfig
	Table      *TableLookupConfig
}

func (d FactorDefinition) ValidateMVP() error {
	switch d.Type {
	case FactorTypeEventField:
		if d.EventField == nil || d.EventField.SourcePath == "" {
			return fmt.Errorf("factor %s: missing event field config", d.Code)
		}
	case FactorTypeExpression:
		if d.Expression == nil || d.Expression.Expression == "" {
			return fmt.Errorf("factor %s: missing expression config", d.Code)
		}
	case FactorTypeRPC:
		if d.RPC == nil || d.RPC.ProviderCode == "" || d.RPC.Method == "" {
			return fmt.Errorf("factor %s: missing rpc config", d.Code)
		}
	case FactorTypeTable:
		if d.Table == nil || d.Table.TableCode == "" || len(d.Table.LookupKey) == 0 {
			return fmt.Errorf("factor %s: missing table config", d.Code)
		}
	default:
		return fmt.Errorf("factor %s: unsupported factor type %s", d.Code, d.Type)
	}
	return nil
}

type TypedFactorContext struct {
	Factors map[string]FactorValue
}

type ExpressionEvalContext struct {
	Params map[string]any
}

type FactorCatalog map[string]FactorDefinition

func (c FactorCatalog) Get(code string) (FactorDefinition, bool) {
	def, ok := c[code]
	return def, ok
}

func (c FactorCatalog) Has(code string) bool {
	_, ok := c[code]
	return ok
}

type ResolveRequest struct {
	Factor            FactorDefinition
	Catalog           FactorCatalog
	Event             map[string]any
	Values            map[string]FactorValue
	ResolveDependency func(ctx context.Context, code string) (FactorValue, error)
	RPCProvider       RPCProvider
	TableProvider     TableProvider
}

type FactorResolver interface {
	Type() FactorType
	Validate(def FactorDefinition, catalog FactorCatalog) error
	Dependencies(def FactorDefinition, catalog FactorCatalog) ([]string, error)
	Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error)
}
