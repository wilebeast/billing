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
	ioPool   *taskPool
	cpuPool  *taskPool
}

func NewFactorExecutor(registry *Registry, deps ExecutorDependencies, opts EngineOptions) *FactorExecutor {
	return &FactorExecutor{
		registry: registry,
		deps:     deps,
		ioPool:   newTaskPool(executorKindIO, opts.FactorIOWorkers),
		cpuPool:  newTaskPool(executorKindCPU, opts.RuleCPUWorkers),
	}
}

func (e *FactorExecutor) ResolveFactors(
	ctx context.Context,
	calcCtx CalculationContext,
	targetCodes []FactorCode,
) (map[FactorCode]FactorValue, error) {
	values := NewInMemoryFactorValueStore()
	plan, err := e.buildExecutionPlan(calcCtx, targetCodes)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ready := append([]*factorPlanNode(nil), plan.roots...)
	completions := make(chan planNodeCompletion, len(plan.nodes))
	inFlight := 0

	for len(ready) > 0 || inFlight > 0 {
		for len(ready) > 0 {
			node := ready[0]
			ready = ready[1:]
			if err := e.submitPlanNode(ctx, calcCtx, values, node, completions); err != nil {
				cancel()
				return nil, err
			}
			inFlight++
		}

		if inFlight == 0 {
			break
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case completion := <-completions:
			inFlight--
			value, err := completion.value, completion.err
			if err != nil && completion.node.definition.DefaultValue != nil {
				defaulted, defaultErr := buildDefaultedFactorValue(completion.node.definition)
				if defaultErr == nil {
					value = defaulted
					err = nil
				}
			}
			if err != nil {
				cancel()
				return nil, err
			}

			values.Set(completion.node.key, value)
			for _, dependentKey := range completion.node.dependents {
				dependent := plan.nodes[dependentKey]
				dependent.pendingDeps--
				if dependent.pendingDeps == 0 {
					ready = append(ready, dependent)
				}
			}
		}
	}

	allValues := values.All()
	result := make(map[FactorCode]FactorValue, len(allValues))
	for _, value := range allValues {
		result[value.Code] = value
	}
	return result, nil
}

func (e *FactorExecutor) submitPlanNode(
	ctx context.Context,
	calcCtx CalculationContext,
	values FactorValueStore,
	node *factorPlanNode,
	completions chan<- planNodeCompletion,
) error {
	run := func(runCtx context.Context) (FactorValue, error) {
		return node.factor.Resolve(runCtx, ResolveContext{
			Event: calcCtx,
			ResolveDependency: func(ctx context.Context, dependency FactorCode) (FactorValue, error) {
				dependencyNode, ok := node.dependencies[dependency]
				if !ok {
					return FactorValue{}, fmt.Errorf("factor %s dependency %s not planned", node.definition.Code, dependency)
				}
				value, ok := values.Get(dependencyNode.key)
				if !ok {
					return FactorValue{}, fmt.Errorf("factor %s dependency %s not resolved", node.definition.Code, dependency)
				}
				return value, nil
			},
			RPCProvider:   e.deps.RPCProvider,
			TableProvider: e.deps.TableProvider,
			RuleProvider:  e.deps.RuleProvider,
		})
	}
	complete := func(value FactorValue, err error) {
		completions <- planNodeCompletion{
			node:  node,
			value: value,
			err:   err,
		}
	}

	switch executorKindForFactor(node.definition) {
	case executorKindIO:
		e.ioPool.Dispatch(ctx, run, complete)
		return nil
	case executorKindCPU:
		e.cpuPool.Dispatch(ctx, run, complete)
		return nil
	default:
		value, err := run(ctx)
		complete(value, err)
		return nil
	}
}

func (e *FactorExecutor) buildExecutionPlan(calcCtx CalculationContext, targetCodes []FactorCode) (*factorExecutionPlan, error) {
	plan := &factorExecutionPlan{
		nodes: map[FactorInstanceKey]*factorPlanNode{},
	}
	visiting := map[FactorInstanceKey]struct{}{}
	for _, code := range targetCodes {
		if _, err := e.buildPlanNode(calcCtx, code, plan, visiting); err != nil {
			return nil, err
		}
	}
	for _, node := range plan.nodes {
		node.pendingDeps = len(node.dependencies)
		if node.pendingDeps == 0 {
			plan.roots = append(plan.roots, node)
		}
	}
	return plan, nil
}

func (e *FactorExecutor) buildPlanNode(
	calcCtx CalculationContext,
	code FactorCode,
	plan *factorExecutionPlan,
	visiting map[FactorInstanceKey]struct{},
) (*factorPlanNode, error) {
	factor, ok := e.registry.Get(code)
	if !ok {
		return nil, fmt.Errorf("factor %s not defined", code)
	}
	def := factor.Definition()
	key := calcCtx.FactorInstanceKey(def)
	if _, ok := visiting[key]; ok {
		return nil, fmt.Errorf("factor dependency cycle detected at %s", key.Code)
	}
	if node, ok := plan.nodes[key]; ok {
		return node, nil
	}

	visiting[key] = struct{}{}
	defer delete(visiting, key)

	node := &factorPlanNode{
		key:          key,
		factor:       factor,
		definition:   def,
		dependencies: map[FactorCode]*factorPlanNode{},
	}
	plan.nodes[key] = node

	for _, depCode := range factor.Dependencies() {
		depNode, err := e.buildPlanNode(calcCtx, depCode, plan, visiting)
		if err != nil {
			return nil, err
		}
		node.dependencies[depCode] = depNode
		depNode.dependents = append(depNode.dependents, key)
	}

	return node, nil
}

func executorKindForFactor(def FactorDefinition) executorKind {
	switch def.Type {
	case FactorTypeRPC, FactorTypeTableLookup:
		return executorKindIO
	case FactorTypeExpression, FactorTypeRuleTable, FactorTypeRateMatch:
		return executorKindCPU
	default:
		return executorKindInline
	}
}

type factorExecutionPlan struct {
	nodes map[FactorInstanceKey]*factorPlanNode
	roots []*factorPlanNode
}

type factorPlanNode struct {
	key          FactorInstanceKey
	factor       Factor
	definition   FactorDefinition
	dependencies map[FactorCode]*factorPlanNode
	dependents   []FactorInstanceKey
	pendingDeps  int
}

type planNodeCompletion struct {
	node  *factorPlanNode
	value FactorValue
	err   error
}
