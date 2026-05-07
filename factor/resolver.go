package factor

import (
	"context"
	"fmt"
	"strconv"
)

type Engine struct {
	definitions map[string]FactorDefinition
	rpcProvider RPCProvider
	table       TableProvider
}

func NewEngine(definitions []FactorDefinition, rpcProvider RPCProvider, table TableProvider) (*Engine, error) {
	defs := make(map[string]FactorDefinition, len(definitions))
	for _, def := range definitions {
		if err := def.ValidateMVP(); err != nil {
			return nil, err
		}
		defs[def.Code] = def
	}

	return &Engine{
		definitions: defs,
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

	value, err := e.resolveByType(ctx, event, def, resolved, visiting)
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

func (e *Engine) resolveByType(
	ctx context.Context,
	event map[string]any,
	def FactorDefinition,
	resolved map[string]FactorValue,
	visiting map[string]bool,
) (FactorValue, error) {
	switch def.Type {
	case FactorTypeEventField:
		raw, ok, err := getByPath(event, def.EventField.SourcePath)
		if err != nil {
			return FactorValue{}, err
		}
		if !ok {
			return FactorValue{}, fmt.Errorf("factor %s path %s missing", def.Code, def.EventField.SourcePath)
		}
		if raw == nil {
			return FactorValue{
				FactorCode: def.Code,
				FactorType: def.Type,
				DataType:   def.DataType,
				Status:     FactorStatusNull,
				Value:      nil,
				ValueText:  "null",
			}, nil
		}
		return buildFactorValue(def, FactorStatusOK, raw)
	case FactorTypeExpression:
		params := map[string]any{}
		deps := def.Expression.DependFactors
		if len(deps) == 0 {
			var err error
			deps, err = expressionVariables(def.Expression.Expression)
			if err != nil {
				return FactorValue{}, err
			}
		}
		for _, dep := range deps {
			value, err := e.resolveOne(ctx, event, dep, resolved, visiting)
			if err != nil {
				return FactorValue{}, err
			}
			if !isScalarStatusOK(value) {
				return FactorValue{}, fmt.Errorf("expression factor %s depends on unusable factor %s status=%s", def.Code, dep, value.Status)
			}
			params[dep] = value.Value
		}
		raw, err := evaluateExpression(def.Expression.Expression, params)
		if err != nil {
			return FactorValue{}, err
		}
		return buildFactorValue(def, FactorStatusOK, raw)
	case FactorTypeRPC:
		input, err := e.resolveMappedInputs(ctx, event, def.RPC.InputMapping, resolved, visiting)
		if err != nil {
			return FactorValue{}, err
		}
		raw, err := e.rpcProvider.Call(ctx, def.RPC.ProviderCode, def.RPC.Method, input)
		if err != nil {
			return FactorValue{}, err
		}
		if def.RPC.OutputPath != "" {
			raw, _, err = getByPath(raw, def.RPC.OutputPath)
			if err != nil {
				return FactorValue{}, err
			}
		}
		return buildFactorValue(def, FactorStatusOK, raw)
	case FactorTypeTable:
		key, err := e.resolveMappedInputs(ctx, event, def.Table.LookupKey, resolved, visiting)
		if err != nil {
			return FactorValue{}, err
		}
		raw, err := e.table.Lookup(ctx, def.Table.TableCode, key)
		if err != nil {
			return FactorValue{}, err
		}
		if def.Table.OutputPath != "" {
			raw, _, err = getByPath(raw, def.Table.OutputPath)
			if err != nil {
				return FactorValue{}, err
			}
		}
		return buildFactorValue(def, FactorStatusOK, raw)
	default:
		return FactorValue{}, fmt.Errorf("factor %s type %s not supported", def.Code, def.Type)
	}
}

func (e *Engine) resolveMappedInputs(
	ctx context.Context,
	event map[string]any,
	mapping map[string]string,
	resolved map[string]FactorValue,
	visiting map[string]bool,
) (map[string]any, error) {
	input := make(map[string]any, len(mapping))
	for target, source := range mapping {
		if def, ok := e.definitions[source]; ok {
			value, err := e.resolveOne(ctx, event, source, resolved, visiting)
			if err != nil {
				return nil, err
			}
			if !isScalarStatusOK(value) {
				return nil, fmt.Errorf("mapped input %s=%s is not usable: %s", target, source, value.Status)
			}
			if def.DataType == FactorDataTypeStruct || def.DataType == FactorDataTypeList || def.DataType == FactorDataTypeMap {
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
