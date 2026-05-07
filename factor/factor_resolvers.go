package factor

import (
	"context"
	"fmt"
)

type EventFieldResolver struct{}

func (r EventFieldResolver) Type() FactorType { return FactorTypeEventField }

func (r EventFieldResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	if def.EventField == nil || def.EventField.SourcePath == "" {
		return fmt.Errorf("factor %s: missing event field config", def.Code)
	}
	return nil
}

func (r EventFieldResolver) Dependencies(_ FactorDefinition, _ FactorCatalog) ([]string, error) {
	return nil, nil
}

func (r EventFieldResolver) Resolve(_ context.Context, req ResolveRequest) (FactorValue, error) {
	raw, ok, err := getByPath(req.Event, req.Factor.EventField.SourcePath)
	if err != nil {
		return FactorValue{}, err
	}
	if !ok {
		return FactorValue{}, fmt.Errorf("factor %s path %s missing", req.Factor.Code, req.Factor.EventField.SourcePath)
	}
	if raw == nil {
		return FactorValue{
			FactorCode: req.Factor.Code,
			FactorType: req.Factor.Type,
			DataType:   req.Factor.DataType,
			Status:     FactorStatusNull,
			Value:      nil,
			ValueText:  "null",
		}, nil
	}
	return buildFactorValue(req.Factor, FactorStatusOK, raw)
}

type ExpressionResolver struct{}

func (r ExpressionResolver) Type() FactorType { return FactorTypeExpression }

func (r ExpressionResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	if def.Expression == nil || def.Expression.Expression == "" {
		return fmt.Errorf("factor %s: missing expression config", def.Code)
	}
	return nil
}

func (r ExpressionResolver) Dependencies(def FactorDefinition, _ FactorCatalog) ([]string, error) {
	if def.Expression == nil {
		return nil, fmt.Errorf("factor %s: missing expression config", def.Code)
	}
	if len(def.Expression.DependFactors) > 0 {
		return def.Expression.DependFactors, nil
	}
	return expressionVariables(def.Expression.Expression)
}

func (r ExpressionResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
	deps := req.Factor.Expression.DependFactors
	if len(deps) == 0 {
		var err error
		deps, err = expressionVariables(req.Factor.Expression.Expression)
		if err != nil {
			return FactorValue{}, err
		}
	}

	params := map[string]any{}
	for _, dep := range deps {
		value, err := req.ResolveDependency(ctx, dep)
		if err != nil {
			return FactorValue{}, err
		}
		if !isScalarStatusOK(value) {
			return FactorValue{}, fmt.Errorf("expression factor %s depends on unusable factor %s status=%s", req.Factor.Code, dep, value.Status)
		}
		params[dep] = value.Value
	}

	raw, err := evaluateExpression(req.Factor.Expression.Expression, params)
	if err != nil {
		return FactorValue{}, err
	}
	return buildFactorValue(req.Factor, FactorStatusOK, raw)
}

type RPCResolver struct{}

func (r RPCResolver) Type() FactorType { return FactorTypeRPC }

func (r RPCResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	if def.RPC == nil || def.RPC.ProviderCode == "" || def.RPC.Method == "" {
		return fmt.Errorf("factor %s: missing rpc config", def.Code)
	}
	return nil
}

func (r RPCResolver) Dependencies(def FactorDefinition, catalog FactorCatalog) ([]string, error) {
	return mappingDependencies(def.RPC.InputMapping, catalog), nil
}

func (r RPCResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
	inputs, err := resolveMappedFactorInputs(ctx, req.Catalog, req.Event, req.Factor.RPC.InputMapping, req.ResolveDependency)
	if err != nil {
		return FactorValue{}, err
	}

	response, err := req.RPCProvider.Call(ctx, RPCRequest{
		ProviderCode: req.Factor.RPC.ProviderCode,
		Method:       req.Factor.RPC.Method,
		Inputs:       inputs,
	})
	if err != nil {
		return FactorValue{}, err
	}

	raw := any(response.Payload)
	if req.Factor.RPC.OutputPath != "" {
		raw, _, err = getByPath(raw, req.Factor.RPC.OutputPath)
		if err != nil {
			return FactorValue{}, err
		}
	}
	return buildFactorValue(req.Factor, FactorStatusOK, raw)
}

type TableLookupResolver struct{}

func (r TableLookupResolver) Type() FactorType { return FactorTypeTable }

func (r TableLookupResolver) Validate(def FactorDefinition, _ FactorCatalog) error {
	if def.Table == nil || def.Table.TableCode == "" || len(def.Table.LookupKey) == 0 {
		return fmt.Errorf("factor %s: missing table config", def.Code)
	}
	return nil
}

func (r TableLookupResolver) Dependencies(def FactorDefinition, catalog FactorCatalog) ([]string, error) {
	return mappingDependencies(def.Table.LookupKey, catalog), nil
}

func (r TableLookupResolver) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
	key, err := resolveMappedInputs(ctx, req.Catalog, req.Event, req.Factor.Table.LookupKey, req.ResolveDependency)
	if err != nil {
		return FactorValue{}, err
	}

	raw, err := req.TableProvider.Lookup(ctx, req.Factor.Table.TableCode, key)
	if err != nil {
		return FactorValue{}, err
	}
	if req.Factor.Table.OutputPath != "" {
		raw, _, err = getByPath(raw, req.Factor.Table.OutputPath)
		if err != nil {
			return FactorValue{}, err
		}
	}
	return buildFactorValue(req.Factor, FactorStatusOK, raw)
}

func mappingDependencies(mapping map[string]string, catalog FactorCatalog) []string {
	deps := make([]string, 0, len(mapping))
	for _, source := range mapping {
		if catalog.Has(source) {
			deps = append(deps, source)
		}
	}
	return deps
}
