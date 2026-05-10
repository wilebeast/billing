package factor

import (
	"context"
	"fmt"
)

type Engine struct {
	registry    *Registry
	rpcProvider RPCProvider
	table       TableProvider
	ruleTable   RuleTableRepository
}

func NewEngine(definitions []FactorDefinition, rpcProvider RPCProvider, table TableProvider) (*Engine, error) {
	registry, err := NewRegistry(
		definitions,
		EventFieldResolver{},
		ExpressionResolver{},
		RPCResolver{},
		TableLookupResolver{},
		RuleTableResolver{},
		RateMatchResolver{},
		ConstantResolver{},
	)
	if err != nil {
		return nil, err
	}

	return &Engine{
		registry:    registry,
		rpcProvider: rpcProvider,
		table:       table,
		ruleTable:   inferRuleTableRepository(table),
	}, nil
}

func (e *Engine) Resolve(ctx context.Context, event map[string]any, targetCodes []string) (TypedFactorContext, error) {
	return e.ResolveWithContext(ctx, NewCalculationContext(event, ChargeObject{}), targetCodes)
}

func (e *Engine) ResolveWithContext(ctx context.Context, calcCtx CalculationContext, targetCodes []string) (TypedFactorContext, error) {
	result := TypedFactorContext{Factors: map[string]FactorValue{}}
	values := NewInMemoryFactorValueStore()
	visiting := map[FactorCode]bool{}

	for _, code := range targetCodes {
		if _, err := e.resolveOne(ctx, calcCtx, FactorCode(code), values, visiting); err != nil {
			return TypedFactorContext{}, err
		}
	}

	for code, value := range values.All() {
		result.Factors[string(code)] = value
	}
	return result, nil
}

func (e *Engine) resolveOne(
	ctx context.Context,
	event NormalizedEvent,
	code FactorCode,
	values FactorValueStore,
	visiting map[FactorCode]bool,
) (FactorValue, error) {
	if value, ok := values.Get(code); ok {
		return value, nil
	}

	factor, ok := e.registry.Get(code)
	if !ok {
		return FactorValue{}, fmt.Errorf("factor %s not defined", code)
	}
	if visiting[code] {
		return FactorValue{}, fmt.Errorf("factor dependency cycle detected at %s", code)
	}

	visiting[code] = true
	defer delete(visiting, code)

	def := factor.Definition()
	value, err := factor.Resolve(ctx, ResolveRequest{
		Factor:  def,
		Catalog: e.registry.Catalog(),
		Event:   event,
		Values:  values,
		ResolveDependency: func(ctx context.Context, dependency FactorCode) (FactorValue, error) {
			return e.resolveOne(ctx, event, dependency, values, visiting)
		},
		RPCProvider:   e.rpcProvider,
		TableProvider: e.table,
		RuleProvider:  e.ruleTable,
	})
	if err != nil {
		if def.DefaultValue != nil {
			defaulted, defaultErr := buildDefaultedFactorValue(def)
			if defaultErr == nil {
				values.Set(code, defaulted)
				return defaulted, nil
			}
		}
		return FactorValue{}, err
	}

	values.Set(code, value)
	return value, nil
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
