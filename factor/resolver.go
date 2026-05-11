package factor

import (
	"context"
	"fmt"
)

type Engine struct {
	registry *Registry
	builder  CalculationContextBuilder
	executor *FactorExecutor
}

func NewEngine(definitions []FactorDefinition, rpcProvider RPCProvider, table TableProvider) (*Engine, error) {
	registry, err := NewRegistry(definitions)
	if err != nil {
		return nil, err
	}

	return &Engine{
		registry: registry,
		builder:  NewCalculationContextBuilder(),
		executor: NewFactorExecutor(registry, ExecutorDependencies{
			RPCProvider:   rpcProvider,
			TableProvider: table,
			RuleProvider:  inferRuleTableRepository(table),
		}),
	}, nil
}

func (e *Engine) Resolve(ctx context.Context, event map[string]any, targetCodes []string) (TypedFactorContext, error) {
	return e.ResolveWithContext(ctx, e.builder.BuildOrder(event), targetCodes)
}

func (e *Engine) ResolveWithContext(ctx context.Context, calcCtx CalculationContext, targetCodes []string) (TypedFactorContext, error) {
	result := TypedFactorContext{Factors: map[string]FactorValue{}}
	codes := make([]FactorCode, 0, len(targetCodes))
	for _, code := range targetCodes {
		codes = append(codes, FactorCode(code))
	}
	values, err := e.executor.ResolveFactors(ctx, calcCtx, codes)
	if err != nil {
		return TypedFactorContext{}, err
	}
	for code, value := range values {
		result.Factors[string(code)] = value
	}
	return result, nil
}

func inferRuleTableRepository(table TableProvider) RuleTableRepository {
	if table == nil {
		return nil
	}
	repo, ok := table.(RuleTableRepository)
	if !ok {
		return nil
	}
	return repo
}

func resolveMappedInputs(
	ctx context.Context,
	catalog FactorCatalog,
	event NormalizedEvent,
	mapping map[string]string,
	resolveDependency func(ctx context.Context, code FactorCode) (FactorValue, error),
) (map[string]any, error) {
	input := make(map[string]any, len(mapping))
	for target, source := range mapping {
		if resolveDependency != nil && catalog.Has(source) {
			value, err := resolveDependency(ctx, FactorCode(source))
			if err != nil {
				return nil, err
			}
			if !isScalarStatusOK(value) {
				return nil, fmt.Errorf("mapped input %s=%s is not usable: %s", target, source, value.Status)
			}
			if value.DataType == FactorDataTypeObject || value.DataType == FactorDataTypeArray {
				return nil, fmt.Errorf("mapped input %s=%s must be scalar", target, source)
			}
			param, err := value.ToExpressionParam()
			if err != nil {
				return nil, err
			}
			input[target] = param
			continue
		}

		raw, ok, err := event.GetByPath(source)
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
	event NormalizedEvent,
	mapping map[string]string,
	resolveDependency func(ctx context.Context, code FactorCode) (FactorValue, error),
) (map[string]FactorValue, error) {
	input := make(map[string]FactorValue, len(mapping))
	for target, source := range mapping {
		if resolveDependency == nil || !catalog.Has(source) {
			raw, ok, err := event.GetByPath(source)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("mapped input %s source %s missing", target, source)
			}
			input[target] = FactorValue{
				Code:       FactorCode(source),
				FactorType: FactorTypeEventField,
				Status:     FactorStatusOK,
				Value:      raw,
				RawValue:   raw,
				ValueText:  stableValueText(raw),
			}
			continue
		}

		value, err := resolveDependency(ctx, FactorCode(source))
		if err != nil {
			return nil, err
		}
		if !isScalarStatusOK(value) {
			return nil, fmt.Errorf("mapped input %s=%s is not usable: %s", target, source, value.Status)
		}
		if value.DataType == FactorDataTypeObject || value.DataType == FactorDataTypeArray {
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
	case FactorDataTypeObject, FactorDataTypeArray:
		return false
	default:
		return true
	}
}

func buildFactorValue(def FactorDefinition, raw any, source FactorSource) (FactorValue, error) {
	normalized, err := NormalizeRawValue(raw, def.DataType)
	if err != nil {
		return FactorValue{}, fmt.Errorf("factor %s normalize: %w", def.Code, err)
	}
	return NewOKFactorValue(def.Code, def.DataType, normalized, raw, source), nil
}

func buildDefaultedFactorValue(def FactorDefinition) (FactorValue, error) {
	source := FactorSource{
		FactorType: def.Type,
		Scope:      def.Scope,
		Version:    def.Version,
	}
	normalized, err := NormalizeRawValue(*def.DefaultValue, def.DataType)
	if err != nil {
		return FactorValue{}, fmt.Errorf("factor %s default normalize: %w", def.Code, err)
	}
	return NewDefaultedFactorValue(def.Code, def.Type, def.DataType, normalized, *def.DefaultValue, source), nil
}
