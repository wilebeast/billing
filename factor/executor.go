package factor

import (
	"context"
	"fmt"
)

type ExecutorDependencies struct {
	RPCProvider   RPCProvider
	TableProvider TableProvider
	RuleProvider  RuleTableRepository
}

type FactorExecutor struct {
	registry *Registry
	deps     ExecutorDependencies
}

func NewFactorExecutor(registry *Registry, deps ExecutorDependencies) *FactorExecutor {
	return &FactorExecutor{
		registry: registry,
		deps:     deps,
	}
}

func (e *FactorExecutor) ResolveFactors(
	ctx context.Context,
	calcCtx CalculationContext,
	targetCodes []FactorCode,
) (map[FactorCode]FactorValue, error) {
	values := NewInMemoryFactorValueStore()
	visiting := map[FactorInstanceKey]bool{}

	for _, code := range targetCodes {
		if _, err := e.resolveOne(ctx, calcCtx, code, values, visiting); err != nil {
			return nil, err
		}
	}

	allValues := values.All()
	result := make(map[FactorCode]FactorValue, len(allValues))
	for _, value := range allValues {
		result[value.Code] = value
	}
	return result, nil
}

func (e *FactorExecutor) resolveOne(
	ctx context.Context,
	calcCtx CalculationContext,
	code FactorCode,
	values FactorValueStore,
	visiting map[FactorInstanceKey]bool,
) (FactorValue, error) {
	factor, ok := e.registry.Get(code)
	if !ok {
		return FactorValue{}, fmt.Errorf("factor %s not defined", code)
	}
	def := factor.Definition()
	key := calcCtx.FactorInstanceKey(def)
	if value, ok := values.Get(key); ok {
		return value, nil
	}
	if visiting[key] {
		return FactorValue{}, fmt.Errorf("factor dependency cycle detected at %s", code)
	}

	visiting[key] = true
	defer delete(visiting, key)

	value, err := factor.Resolve(ctx, ResolveContext{
		Event: calcCtx,
		ResolveDependency: func(ctx context.Context, dependency FactorCode) (FactorValue, error) {
			return e.resolveOne(ctx, calcCtx, dependency, values, visiting)
		},
		RPCProvider:   e.deps.RPCProvider,
		TableProvider: e.deps.TableProvider,
		RuleProvider:  e.deps.RuleProvider,
	})
	if err != nil {
		if def.DefaultValue != nil {
			defaulted, defaultErr := buildDefaultedFactorValue(def)
			if defaultErr == nil {
				values.Set(key, defaulted)
				return defaulted, nil
			}
		}
		return FactorValue{}, err
	}

	values.Set(key, value)
	return value, nil
}
