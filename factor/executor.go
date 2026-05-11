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
	visiting := map[FactorCode]bool{}

	for _, code := range targetCodes {
		if _, err := e.resolveOne(ctx, calcCtx, code, values, visiting); err != nil {
			return nil, err
		}
	}

	return values.All(), nil
}

func (e *FactorExecutor) resolveOne(
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
	def, ok := e.registry.Definition(code)
	if !ok {
		return FactorValue{}, fmt.Errorf("factor definition %s not found", code)
	}
	if visiting[code] {
		return FactorValue{}, fmt.Errorf("factor dependency cycle detected at %s", code)
	}

	visiting[code] = true
	defer delete(visiting, code)

	value, err := factor.Resolve(ctx, ResolveRequest{
		Factor:  def,
		Catalog: e.registry.Catalog(),
		Event:   event,
		Values:  values,
		ResolveDependency: func(ctx context.Context, dependency FactorCode) (FactorValue, error) {
			return e.resolveOne(ctx, event, dependency, values, visiting)
		},
		RPCProvider:   e.deps.RPCProvider,
		TableProvider: e.deps.TableProvider,
		RuleProvider:  e.deps.RuleProvider,
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
