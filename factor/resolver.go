package factor

import (
	"context"
	"fmt"
	"strconv"
)

type Engine struct {
	definitions map[string]FactorDefinition
	resolvers   map[FactorType]FactorResolver
	rpcProvider RPCProvider
	table       TableProvider
}

func NewEngine(definitions []FactorDefinition, rpcProvider RPCProvider, table TableProvider) (*Engine, error) {
	resolvers := map[FactorType]FactorResolver{
		FactorTypeEventField: EventFieldResolver{},
		FactorTypeExpression: ExpressionResolver{},
		FactorTypeRPC:        RPCResolver{},
		FactorTypeTable:      TableLookupResolver{},
	}
	defs := make(map[string]FactorDefinition, len(definitions))
	catalog := FactorCatalog(defs)
	for _, def := range definitions {
		resolver, ok := resolvers[def.Type]
		if !ok {
			return nil, fmt.Errorf("factor %s: unsupported factor type %s", def.Code, def.Type)
		}
		if err := resolver.Validate(def, catalog); err != nil {
			return nil, err
		}
		defs[def.Code] = def
	}

	return &Engine{
		definitions: defs,
		resolvers:   resolvers,
		rpcProvider: rpcProvider,
		table:       table,
	}, nil
}

func (e *Engine) Resolve(ctx context.Context, event map[string]any, targetCodes []string) (TypedFactorContext, error) {
	result := TypedFactorContext{Factors: map[string]FactorValue{}}
	visiting := map[string]bool{}

	for _, code := range targetCodes {
		if _, err := e.resolveOne(ctx, event, code, result.Factors, visiting); err != nil {
			return TypedFactorContext{}, err
		}
	}

	return result, nil
}

func (e *Engine) resolveOne(
	ctx context.Context,
	event map[string]any,
	code string,
	resolved map[string]FactorValue,
	visiting map[string]bool,
) (FactorValue, error) {
	if value, ok := resolved[code]; ok {
		return value, nil
	}

	def, ok := e.definitions[code]
	if !ok {
		return FactorValue{}, fmt.Errorf("factor %s not defined", code)
	}
	if visiting[code] {
		return FactorValue{}, fmt.Errorf("factor dependency cycle detected at %s", code)
	}

	visiting[code] = true
	defer delete(visiting, code)

	resolver, ok := e.resolvers[def.Type]
	if !ok {
		return FactorValue{}, fmt.Errorf("factor %s type %s not supported", def.Code, def.Type)
	}

	value, err := resolver.Resolve(ctx, ResolveRequest{
		Factor:  def,
		Catalog: FactorCatalog(e.definitions),
		Event:   event,
		Values:  resolved,
		ResolveDependency: func(ctx context.Context, dependency string) (FactorValue, error) {
			return e.resolveOne(ctx, event, dependency, resolved, visiting)
		},
		RPCProvider:   e.rpcProvider,
		TableProvider: e.table,
	})
	if err != nil {
		if def.DefaultValue != nil {
			defaulted, defaultErr := buildFactorValue(def, FactorStatusDefaulted, *def.DefaultValue)
			if defaultErr == nil {
				resolved[code] = defaulted
				return defaulted, nil
			}
		}
		return FactorValue{}, err
	}

	resolved[code] = value
	return value, nil
}

func resolveMappedInputs(
	ctx context.Context,
	catalog FactorCatalog,
	event map[string]any,
	mapping map[string]string,
	resolveDependency func(ctx context.Context, code string) (FactorValue, error),
) (map[string]any, error) {
	input := make(map[string]any, len(mapping))
	for target, source := range mapping {
		if resolveDependency != nil && catalog.Has(source) {
			value, err := resolveDependency(ctx, source)
			if err != nil {
				return nil, err
			}
			if !isScalarStatusOK(value) {
				return nil, fmt.Errorf("mapped input %s=%s is not usable: %s", target, source, value.Status)
			}
			if value.DataType == FactorDataTypeStruct || value.DataType == FactorDataTypeList || value.DataType == FactorDataTypeMap {
				return nil, fmt.Errorf("mapped input %s=%s must be scalar", target, source)
			}
			input[target] = value.Value
			continue
		}

		raw, ok, err := getByPath(event, source)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("mapped input %s source %s missing", target, source)
		}
		input[target] = raw
	}
	return input, nil
}

func resolveMappedFactorInputs(
	ctx context.Context,
	catalog FactorCatalog,
	event map[string]any,
	mapping map[string]string,
	resolveDependency func(ctx context.Context, code string) (FactorValue, error),
) (map[string]FactorValue, error) {
	input := make(map[string]FactorValue, len(mapping))
	for target, source := range mapping {
		if resolveDependency == nil || !catalog.Has(source) {
			raw, ok, err := getByPath(event, source)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("mapped input %s source %s missing", target, source)
			}
			input[target] = FactorValue{
				FactorCode: source,
				FactorType: FactorTypeEventField,
				Status:     FactorStatusOK,
				Value:      raw,
				ValueText:  stableValueText(raw),
			}
			continue
		}

		value, err := resolveDependency(ctx, source)
		if err != nil {
			return nil, err
		}
		if !isScalarStatusOK(value) {
			return nil, fmt.Errorf("mapped input %s=%s is not usable: %s", target, source, value.Status)
		}
		if value.DataType == FactorDataTypeStruct || value.DataType == FactorDataTypeList || value.DataType == FactorDataTypeMap {
			return nil, fmt.Errorf("mapped input %s=%s must be scalar", target, source)
		}
		input[target] = value
	}
	return input, nil
}

func isScalarStatusOK(value FactorValue) bool {
	if value.Status != FactorStatusOK && value.Status != FactorStatusDefaulted {
		return false
	}
	switch value.DataType {
	case FactorDataTypeStruct, FactorDataTypeList, FactorDataTypeMap:
		return false
	default:
		return true
	}
}

func buildFactorValue(def FactorDefinition, status FactorStatus, raw any) (FactorValue, error) {
	normalized, err := normalizeValue(def.DataType, raw)
	if err != nil {
		return FactorValue{}, fmt.Errorf("factor %s normalize: %w", def.Code, err)
	}
	return FactorValue{
		FactorCode: def.Code,
		FactorType: def.Type,
		DataType:   def.DataType,
		Status:     status,
		Value:      normalized,
		ValueText:  stableValueText(normalized),
	}, nil
}

func normalizeValue(dataType FactorDataType, raw any) (any, error) {
	switch dataType {
	case FactorDataTypeDecimal:
		switch v := raw.(type) {
		case string:
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, err
			}
			return f, nil
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		case float64:
			return v, nil
		default:
			return nil, fmt.Errorf("unsupported decimal source %T", raw)
		}
	case FactorDataTypeString:
		switch v := raw.(type) {
		case string:
			return v, nil
		default:
			return stableValueText(v), nil
		}
	case FactorDataTypeInt:
		switch v := raw.(type) {
		case int:
			return v, nil
		case int64:
			return int(v), nil
		case float64:
			return int(v), nil
		case string:
			i, err := strconv.Atoi(v)
			if err != nil {
				return nil, err
			}
			return i, nil
		default:
			return nil, fmt.Errorf("unsupported int source %T", raw)
		}
	case FactorDataTypeBool:
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, err
			}
			return b, nil
		default:
			return nil, fmt.Errorf("unsupported bool source %T", raw)
		}
	case FactorDataTypeDatetime, FactorDataTypeStruct, FactorDataTypeList, FactorDataTypeMap:
		return raw, nil
	default:
		return raw, nil
	}
}
