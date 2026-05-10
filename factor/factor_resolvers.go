package factor

import (
	"context"
	"fmt"
	"time"
)

type EventFieldResolver struct{}

func (r EventFieldResolver) Type() FactorType { return FactorTypeEventField }

func (r EventFieldResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	cfg, err := def.eventFieldConfig()
	if err != nil {
		return fmt.Errorf("factor %s: decode event field config: %w", def.Code, err)
	}
	if cfg == nil || cfg.SourcePath == "" {
		return fmt.Errorf("factor %s: missing event field config", def.Code)
	}
	return nil
}

func (r EventFieldResolver) Dependencies(_ FactorDefinition, _ FactorCatalog) ([]FactorCode, error) {
	return nil, nil
}

func (r EventFieldResolver) Resolve(_ context.Context, req ResolveRequest) (FactorValue, error) {
	cfg, err := req.Factor.eventFieldConfig()
	if err != nil {
		return FactorValue{}, err
	}
	source := FactorSource{
		FactorType: req.Factor.Type,
		Scope:      req.Factor.Scope,
		SourcePath: cfg.SourcePath,
		Version:    req.Factor.Version,
	}
	raw, ok, err := getScopedValue(req.Event, req.Factor.Scope, cfg.SourcePath)
	if err != nil {
		return NewFailedFactorValue(req.Factor.Code, req.Factor.DataType, source, "FIELD_READ_ERROR", err.Error()), nil
	}
	if !ok {
		if req.Factor.DefaultValue != nil {
			return buildDefaultedFactorValue(req.Factor)
		}
		return NewMissingFactorValue(req.Factor.Code, req.Factor.DataType, source), nil
	}
	if raw == nil {
		return NewNullFactorValue(req.Factor.Code, req.Factor.DataType, source), nil
	}
	value, err := NormalizeRawValue(raw, req.Factor.DataType)
	if err != nil {
		return NewInvalidFactorValue(req.Factor.Code, req.Factor.DataType, raw, source, err.Error()), nil
	}
	return NewOKFactorValue(req.Factor.Code, req.Factor.DataType, value, raw, source), nil
}

type ExpressionResolver struct{}

func (r ExpressionResolver) Type() FactorType { return FactorTypeExpression }

func (r ExpressionResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	cfg, err := def.expressionConfig()
	if err != nil {
		return fmt.Errorf("factor %s: decode expression config: %w", def.Code, err)
	}
	if cfg == nil || cfg.Expression == "" {
		return fmt.Errorf("factor %s: missing expression config", def.Code)
	}
	return nil
}

func (r ExpressionResolver) Dependencies(def FactorDefinition, _ FactorCatalog) ([]FactorCode, error) {
	cfg, err := def.expressionConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("factor %s: missing expression config", def.Code)
	}
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

func (r ExpressionResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
	cfg, err := req.Factor.expressionConfig()
	if err != nil {
		return FactorValue{}, err
	}
	deps := cfg.DependFactors
	if len(deps) == 0 {
		deps, err = expressionVariables(cfg.Expression)
		if err != nil {
			return FactorValue{}, err
		}
	}

	params := map[string]any{}
	for _, dep := range deps {
		value, err := req.ResolveDependency(ctx, FactorCode(dep))
		if err != nil {
			return FactorValue{}, err
		}
		if !isScalarStatusOK(value) {
			return FactorValue{}, fmt.Errorf("expression factor %s depends on unusable factor %s status=%s", req.Factor.Code, dep, value.Status)
		}
		param, err := value.ToExpressionParam()
		if err != nil {
			return FactorValue{}, err
		}
		params[dep] = param
	}

	raw, err := evaluateExpression(cfg.Expression, params)
	if err != nil {
		return FactorValue{}, err
	}
	source := FactorSource{
		FactorType: req.Factor.Type,
		Scope:      req.Factor.Scope,
		Expression: cfg.Expression,
		Version:    req.Factor.Version,
	}
	return buildFactorValue(req.Factor, raw, source)
}

type RPCResolver struct{}

func (r RPCResolver) Type() FactorType { return FactorTypeRPC }

func (r RPCResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	cfg, err := def.rpcConfig()
	if err != nil {
		return fmt.Errorf("factor %s: decode rpc config: %w", def.Code, err)
	}
	if cfg == nil || cfg.ProviderCode == "" || cfg.Method == "" {
		return fmt.Errorf("factor %s: missing rpc config", def.Code)
	}
	return nil
}

func (r RPCResolver) Dependencies(def FactorDefinition, catalog FactorCatalog) ([]FactorCode, error) {
	cfg, err := def.rpcConfig()
	if err != nil {
		return nil, err
	}
	return mappingDependencies(cfg.InputMapping, catalog), nil
}

func (r RPCResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
	cfg, err := req.Factor.rpcConfig()
	if err != nil {
		return FactorValue{}, err
	}
	inputs, err := resolveMappedFactorInputs(ctx, req.Catalog, req.Event, cfg.InputMapping, req.ResolveDependency)
	if err != nil {
		return FactorValue{}, err
	}

	response, err := req.RPCProvider.Call(ctx, RPCRequest{
		ProviderCode: cfg.ProviderCode,
		Method:       cfg.Method,
		Inputs:       inputs,
	})
	if err != nil {
		return FactorValue{}, err
	}

	raw := any(response.Payload)
	if cfg.OutputPath != "" {
		raw, _, err = getByPath(raw, cfg.OutputPath)
		if err != nil {
			return FactorValue{}, err
		}
	}
	source := FactorSource{
		FactorType:   req.Factor.Type,
		Scope:        req.Factor.Scope,
		ProviderCode: cfg.ProviderCode,
		Method:       cfg.Method,
		ResponsePath: cfg.OutputPath,
		Version:      req.Factor.Version,
	}
	return buildFactorValue(req.Factor, raw, source)
}

type TableLookupResolver struct{}

func (r TableLookupResolver) Type() FactorType { return FactorTypeTable }

func (r TableLookupResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	cfg, err := def.tableLookupConfig()
	if err != nil {
		return fmt.Errorf("factor %s: decode table config: %w", def.Code, err)
	}
	if cfg == nil || cfg.TableCode == "" || len(cfg.LookupKey) == 0 {
		return fmt.Errorf("factor %s: missing table config", def.Code)
	}
	return nil
}

func (r TableLookupResolver) Dependencies(def FactorDefinition, catalog FactorCatalog) ([]FactorCode, error) {
	cfg, err := def.tableLookupConfig()
	if err != nil {
		return nil, err
	}
	return mappingDependencies(cfg.LookupKey, catalog), nil
}

func (r TableLookupResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
	cfg, err := req.Factor.tableLookupConfig()
	if err != nil {
		return FactorValue{}, err
	}
	key, err := resolveMappedInputs(ctx, req.Catalog, req.Event, cfg.LookupKey, req.ResolveDependency)
	if err != nil {
		return FactorValue{}, err
	}

	raw, err := req.TableProvider.Lookup(ctx, cfg.TableCode, key)
	if err != nil {
		return FactorValue{}, err
	}
	if cfg.OutputPath != "" {
		raw, _, err = getByPath(raw, cfg.OutputPath)
		if err != nil {
			return FactorValue{}, err
		}
	}
	source := FactorSource{
		FactorType: req.Factor.Type,
		Scope:      req.Factor.Scope,
		TableCode:  cfg.TableCode,
		LookupKey:  key,
		Version:    req.Factor.Version,
	}
	return buildFactorValue(req.Factor, raw, source)
}

type RuleTableResolver struct{}

func (r RuleTableResolver) Type() FactorType { return FactorTypeRuleTable }

func (r RuleTableResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	cfg, err := def.ruleTableConfig()
	if err != nil {
		return fmt.Errorf("factor %s: decode rule table config: %w", def.Code, err)
	}
	if cfg == nil || cfg.RuleTableCode == "" || len(cfg.InputMapping) == 0 {
		return fmt.Errorf("factor %s: missing rule table config", def.Code)
	}
	return nil
}

func (r RuleTableResolver) Dependencies(def FactorDefinition, catalog FactorCatalog) ([]FactorCode, error) {
	cfg, err := def.ruleTableConfig()
	if err != nil {
		return nil, err
	}
	return mappingDependencies(cfg.InputMapping, catalog), nil
}

func (r RuleTableResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
	cfg, err := req.Factor.ruleTableConfig()
	if err != nil {
		return FactorValue{}, err
	}
	if req.RuleProvider == nil {
		return NewFailedFactorValue(
			req.Factor.Code,
			req.Factor.DataType,
			FactorSource{FactorType: req.Factor.Type, Scope: req.Factor.Scope, RuleTableCode: cfg.RuleTableCode},
			"RULE_PROVIDER_MISSING",
			"rule table repository is not configured",
		), nil
	}
	key, err := resolveMappedInputs(ctx, req.Catalog, req.Event, cfg.InputMapping, req.ResolveDependency)
	if err != nil {
		return FactorValue{}, err
	}
	matched, err := req.RuleProvider.Match(ctx, RuleTableMatchRequest{
		RuleTableCode: cfg.RuleTableCode,
		Inputs:        key,
	})
	if err != nil {
		return NewFailedFactorValue(
			req.Factor.Code,
			req.Factor.DataType,
			FactorSource{FactorType: req.Factor.Type, Scope: req.Factor.Scope, RuleTableCode: cfg.RuleTableCode, MatchedInputs: key},
			"RULE_TABLE_CONFLICT",
			err.Error(),
		), nil
	}
	source := FactorSource{
		FactorType:    req.Factor.Type,
		Scope:         req.Factor.Scope,
		RuleTableCode: cfg.RuleTableCode,
		MatchedInputs: key,
		Version:       req.Factor.Version,
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
	raw := matched.OutputValue
	if cfg.OutputPath != "" {
		raw, _, err = getByPath(raw, cfg.OutputPath)
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
	return buildFactorValue(req.Factor, raw, source)
}

type RateMatchResolver struct {
	RuleTableResolver
}

func (r RateMatchResolver) Type() FactorType { return FactorTypeRateMatch }

type ConstantResolver struct{}

func (r ConstantResolver) Type() FactorType { return FactorTypeConstant }

func (r ConstantResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	cfg, err := def.constantConfig()
	if err != nil {
		return fmt.Errorf("factor %s: decode constant config: %w", def.Code, err)
	}
	if cfg == nil {
		return fmt.Errorf("factor %s: missing constant config", def.Code)
	}
	return nil
}

func (r ConstantResolver) Dependencies(_ FactorDefinition, _ FactorCatalog) ([]FactorCode, error) {
	return nil, nil
}

func (r ConstantResolver) Resolve(_ context.Context, req ResolveRequest) (FactorValue, error) {
	cfg, err := req.Factor.constantConfig()
	if err != nil {
		return FactorValue{}, err
	}
	source := FactorSource{
		FactorType:    req.Factor.Type,
		Scope:         req.Factor.Scope,
		ConstantValue: cfg.Value,
		Version:       req.Factor.Version,
	}
	return buildFactorValue(req.Factor, cfg.Value, source)
}

func mappingDependencies(mapping map[string]string, catalog FactorCatalog) []FactorCode {
	deps := make([]FactorCode, 0, len(mapping))
	for _, source := range mapping {
		if catalog.Has(source) {
			deps = append(deps, FactorCode(source))
		}
	}
	return deps
}

func getScopedValue(event NormalizedEvent, scope FactorScope, path string) (any, bool, error) {
	if scoped, ok := event.(ScopedValueGetter); ok {
		return scoped.GetByScope(scope, path)
	}
	return event.GetByPath(path)
}
