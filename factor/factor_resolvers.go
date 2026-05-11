package factor

import (
	"context"
	"fmt"
	"time"
)

type EventFieldFactorProvider struct{}

func (p EventFieldFactorProvider) Type() FactorType { return FactorTypeEventField }
func (p EventFieldFactorProvider) NewFactor(def FactorDefinition, _ FactorCatalog) (Factor, error) {
	cfg, err := def.eventFieldConfig()
	if err != nil {
		return nil, fmt.Errorf("factor %s: decode event field config: %w", def.Code, err)
	}
	if cfg == nil || cfg.SourcePath == "" {
		return nil, fmt.Errorf("factor %s: missing event field config", def.Code)
	}
	return EventFieldFactor{definition: def, config: *cfg}, nil
}

type EventFieldFactor struct {
	definition FactorDefinition
	config     EventFieldConfig
}

func (f EventFieldFactor) Definition() FactorDefinition { return f.definition }

func (f EventFieldFactor) Resolve(_ context.Context, req ResolveContext) (FactorValue, error) {
	source := FactorSource{
		FactorType: f.definition.Type,
		Scope:      f.definition.Scope,
		SourcePath: f.config.SourcePath,
		Version:    f.definition.Version,
	}
	raw, ok, err := getScopedValue(req.Event, f.definition.Scope, f.config.SourcePath)
	if err != nil {
		return NewFailedFactorValue(f.definition.Code, f.definition.DataType, source, "FIELD_READ_ERROR", err.Error()), nil
	}
	if !ok {
		if f.definition.DefaultValue != nil {
			return buildDefaultedFactorValue(f.definition)
		}
		return NewMissingFactorValue(f.definition.Code, f.definition.DataType, source), nil
	}
	if raw == nil {
		return NewNullFactorValue(f.definition.Code, f.definition.DataType, source), nil
	}
	value, err := NormalizeRawValue(raw, f.definition.DataType)
	if err != nil {
		return NewInvalidFactorValue(f.definition.Code, f.definition.DataType, raw, source, err.Error()), nil
	}
	return NewOKFactorValue(f.definition.Code, f.definition.DataType, value, raw, source), nil
}

type ExpressionFactor struct {
	definition   FactorDefinition
	config       ExpressionConfig
	dependencies []FactorCode
}

type ExpressionFactorProvider struct{}

func (p ExpressionFactorProvider) Type() FactorType { return FactorTypeExpression }
func (p ExpressionFactorProvider) NewFactor(def FactorDefinition, _ FactorCatalog) (Factor, error) {
	cfg, err := def.expressionConfig()
	if err != nil {
		return nil, fmt.Errorf("factor %s: decode expression config: %w", def.Code, err)
	}
	if cfg == nil || cfg.Expression == "" {
		return nil, fmt.Errorf("factor %s: missing expression config", def.Code)
	}
	deps, err := expressionDependencies(*cfg)
	if err != nil {
		return nil, err
	}
	return ExpressionFactor{definition: def, config: *cfg, dependencies: deps}, nil
}

func expressionDependencies(cfg ExpressionConfig) ([]FactorCode, error) {
	if len(cfg.DependFactors) > 0 {
		deps := make([]FactorCode, 0, len(cfg.DependFactors))
		for _, dep := range cfg.DependFactors {
			deps = append(deps, FactorCode(dep))
		}
		return deps, nil
	}
	vars, err := expressionVariables(cfg.Expression)
	if err != nil {
		return nil, err
	}
	deps := make([]FactorCode, 0, len(vars))
	for _, dep := range vars {
		deps = append(deps, FactorCode(dep))
	}
	return deps, nil
}

func (f ExpressionFactor) Definition() FactorDefinition { return f.definition }

func (f ExpressionFactor) Resolve(ctx context.Context, req ResolveContext) (FactorValue, error) {
	params := map[string]any{}
	for _, dep := range f.dependencies {
		value, err := req.ResolveDependency(ctx, FactorCode(dep))
		if err != nil {
			return FactorValue{}, err
		}
		if !isScalarStatusOK(value) {
			return FactorValue{}, fmt.Errorf("expression factor %s depends on unusable factor %s status=%s", f.definition.Code, dep, value.Status)
		}
		param, err := value.ToExpressionParam()
		if err != nil {
			return FactorValue{}, err
		}
		params[string(dep)] = param
	}

	raw, err := evaluateExpression(f.config.Expression, params)
	if err != nil {
		return FactorValue{}, err
	}
	source := FactorSource{
		FactorType: f.definition.Type,
		Scope:      f.definition.Scope,
		Expression: f.config.Expression,
		Version:    f.definition.Version,
	}
	return buildFactorValue(f.definition, raw, source)
}

type RPCFactor struct {
	definition    FactorDefinition
	config        RPCConfig
	inputBindings map[string]InputBinding
}

type RPCFactorProvider struct{}

func (p RPCFactorProvider) Type() FactorType { return FactorTypeRPC }
func (p RPCFactorProvider) NewFactor(def FactorDefinition, catalog FactorCatalog) (Factor, error) {
	cfg, err := def.rpcConfig()
	if err != nil {
		return nil, fmt.Errorf("factor %s: decode rpc config: %w", def.Code, err)
	}
	if cfg == nil || cfg.ProviderCode == "" || cfg.Method == "" {
		return nil, fmt.Errorf("factor %s: missing rpc config", def.Code)
	}
	return RPCFactor{
		definition:    def,
		config:        *cfg,
		inputBindings: compileInputBindings(cfg.InputMapping, catalog),
	}, nil
}

func (f RPCFactor) Definition() FactorDefinition { return f.definition }

func (f RPCFactor) Resolve(ctx context.Context, req ResolveContext) (FactorValue, error) {
	inputs, err := resolveMappedFactorInputs(ctx, req.Event, f.inputBindings, req.ResolveDependency)
	if err != nil {
		return FactorValue{}, err
	}

	response, err := req.RPCProvider.Call(ctx, RPCRequest{
		ProviderCode: f.config.ProviderCode,
		Method:       f.config.Method,
		Inputs:       inputs,
	})
	if err != nil {
		return FactorValue{}, err
	}

	raw := any(response.Payload)
	if f.config.OutputPath != "" {
		raw, _, err = getByPath(raw, f.config.OutputPath)
		if err != nil {
			return FactorValue{}, err
		}
	}
	source := FactorSource{
		FactorType:   f.definition.Type,
		Scope:        f.definition.Scope,
		ProviderCode: f.config.ProviderCode,
		Method:       f.config.Method,
		ResponsePath: f.config.OutputPath,
		Version:      f.definition.Version,
	}
	return buildFactorValue(f.definition, raw, source)
}

type TableLookupFactor struct {
	definition    FactorDefinition
	config        TableLookupConfig
	inputBindings map[string]InputBinding
}

type TableLookupFactorProvider struct{}

func (p TableLookupFactorProvider) Type() FactorType { return FactorTypeTableLookup }
func (p TableLookupFactorProvider) NewFactor(def FactorDefinition, catalog FactorCatalog) (Factor, error) {
	cfg, err := def.tableLookupConfig()
	if err != nil {
		return nil, fmt.Errorf("factor %s: decode table config: %w", def.Code, err)
	}
	if cfg == nil || cfg.TableCode == "" || len(cfg.LookupKey) == 0 {
		return nil, fmt.Errorf("factor %s: missing table config", def.Code)
	}
	return TableLookupFactor{
		definition:    def,
		config:        *cfg,
		inputBindings: compileInputBindings(cfg.LookupKey, catalog),
	}, nil
}

func (f TableLookupFactor) Definition() FactorDefinition { return f.definition }

func (f TableLookupFactor) Resolve(ctx context.Context, req ResolveContext) (FactorValue, error) {
	key, err := resolveMappedInputs(ctx, req.Event, f.inputBindings, req.ResolveDependency)
	if err != nil {
		return FactorValue{}, err
	}

	raw, err := req.TableProvider.Lookup(ctx, f.config.TableCode, key)
	if err != nil {
		return FactorValue{}, err
	}
	if f.config.OutputPath != "" {
		raw, _, err = getByPath(raw, f.config.OutputPath)
		if err != nil {
			return FactorValue{}, err
		}
	}
	source := FactorSource{
		FactorType: f.definition.Type,
		Scope:      f.definition.Scope,
		TableCode:  f.config.TableCode,
		LookupKey:  key,
		Version:    f.definition.Version,
	}
	return buildFactorValue(f.definition, raw, source)
}

type RuleTableFactor struct {
	definition    FactorDefinition
	config        RuleTableConfig
	inputBindings map[string]InputBinding
}

type RuleTableFactorProvider struct{}

func (p RuleTableFactorProvider) Type() FactorType { return FactorTypeRuleTable }
func (p RuleTableFactorProvider) NewFactor(def FactorDefinition, catalog FactorCatalog) (Factor, error) {
	cfg, err := def.ruleTableConfig()
	if err != nil {
		return nil, fmt.Errorf("factor %s: decode rule table config: %w", def.Code, err)
	}
	if cfg == nil || cfg.RuleTableCode == "" || len(cfg.InputMapping) == 0 {
		return nil, fmt.Errorf("factor %s: missing rule table config", def.Code)
	}
	return RuleTableFactor{
		definition:    def,
		config:        *cfg,
		inputBindings: compileInputBindings(cfg.InputMapping, catalog),
	}, nil
}

func (f RuleTableFactor) Definition() FactorDefinition { return f.definition }

func (f RuleTableFactor) Resolve(ctx context.Context, req ResolveContext) (FactorValue, error) {
	if req.RuleProvider == nil {
		return NewFailedFactorValue(
			f.definition.Code,
			f.definition.DataType,
			FactorSource{FactorType: f.definition.Type, Scope: f.definition.Scope, RuleTableCode: f.config.RuleTableCode},
			"RULE_PROVIDER_MISSING",
			"rule table repository is not configured",
		), nil
	}
	key, err := resolveMappedInputs(ctx, req.Event, f.inputBindings, req.ResolveDependency)
	if err != nil {
		return FactorValue{}, err
	}
	matched, err := req.RuleProvider.Match(ctx, RuleTableMatchRequest{
		RuleTableCode: f.config.RuleTableCode,
		Inputs:        key,
	})
	if err != nil {
		return NewFailedFactorValue(
			f.definition.Code,
			f.definition.DataType,
			FactorSource{FactorType: f.definition.Type, Scope: f.definition.Scope, RuleTableCode: f.config.RuleTableCode, MatchedInputs: key},
			"RULE_TABLE_CONFLICT",
			err.Error(),
		), nil
	}
	source := FactorSource{
		FactorType:    f.definition.Type,
		Scope:         f.definition.Scope,
		RuleTableCode: f.config.RuleTableCode,
		MatchedInputs: key,
		Version:       f.definition.Version,
	}
	if !matched.Found {
		return NewFailedFactorValue(
			f.definition.Code,
			f.definition.DataType,
			source,
			"RULE_TABLE_NO_MATCH",
			"no matched rule row",
		), nil
	}
	raw := matched.OutputValue
	if f.config.OutputPath != "" {
		raw, _, err = getByPath(raw, f.config.OutputPath)
		if err != nil {
			return FactorValue{}, err
		}
	}
	source.MatchedRowID = matched.RowID
	source.Priority = matched.Priority
	source.Version = matched.Version
	source.CostMs = matched.CostMs
	if !matched.MatchedByTime.IsZero() {
		source.MatchedByTime = matched.MatchedByTime.UTC().Format(time.RFC3339)
	}
	return buildFactorValue(f.definition, raw, source)
}

type RateMatchFactor struct {
	RuleTableFactor
}

type RateMatchFactorProvider struct{}

func (p RateMatchFactorProvider) Type() FactorType { return FactorTypeRateMatch }
func (p RateMatchFactorProvider) NewFactor(def FactorDefinition, catalog FactorCatalog) (Factor, error) {
	cfg, err := def.ruleTableConfig()
	if err != nil {
		return nil, fmt.Errorf("factor %s: decode rate match config: %w", def.Code, err)
	}
	if cfg == nil || cfg.RuleTableCode == "" || len(cfg.InputMapping) == 0 {
		return nil, fmt.Errorf("factor %s: missing rate match config", def.Code)
	}
	return RateMatchFactor{RuleTableFactor: RuleTableFactor{
		definition:    def,
		config:        *cfg,
		inputBindings: compileInputBindings(cfg.InputMapping, catalog),
	}}, nil
}

type ConstantFactor struct {
	definition FactorDefinition
	config     ConstantConfig
}

type ConstantFactorProvider struct{}

func (p ConstantFactorProvider) Type() FactorType { return FactorTypeConstant }
func (p ConstantFactorProvider) NewFactor(def FactorDefinition, _ FactorCatalog) (Factor, error) {
	cfg, err := def.constantConfig()
	if err != nil {
		return nil, fmt.Errorf("factor %s: decode constant config: %w", def.Code, err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("factor %s: missing constant config", def.Code)
	}
	return ConstantFactor{definition: def, config: *cfg}, nil
}

func (f ConstantFactor) Definition() FactorDefinition { return f.definition }

func (f ConstantFactor) Resolve(_ context.Context, _ ResolveContext) (FactorValue, error) {
	source := FactorSource{
		FactorType:    f.definition.Type,
		Scope:         f.definition.Scope,
		ConstantValue: f.config.Value,
		Version:       f.definition.Version,
	}
	return buildFactorValue(f.definition, f.config.Value, source)
}

func compileInputBindings(mapping map[string]string, catalog FactorCatalog) map[string]InputBinding {
	bindings := make(map[string]InputBinding, len(mapping))
	for target, source := range mapping {
		binding := InputBinding{SourcePath: source}
		if _, ok := catalog.Get(FactorCode(source)); ok {
			binding.Dependency = FactorCode(source)
		}
		bindings[target] = binding
	}
	return bindings
}

func getScopedValue(event NormalizedEvent, scope FactorScope, path string) (any, bool, error) {
	if scoped, ok := event.(ScopedValueGetter); ok {
		return scoped.GetByScope(scope, path)
	}
	return event.GetByPath(path)
}
