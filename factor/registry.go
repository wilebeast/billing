package factor

import (
	"context"
	"fmt"
)

type ResolverRegistry struct {
	resolvers map[FactorType]FactorResolver
}

func NewResolverRegistry(resolvers ...FactorResolver) *ResolverRegistry {
	m := make(map[FactorType]FactorResolver, len(resolvers))
	for _, resolver := range resolvers {
		m[resolver.Type()] = resolver
	}
	return &ResolverRegistry{resolvers: m}
}

func (r *ResolverRegistry) Get(t FactorType) (FactorResolver, bool) {
	if r == nil {
		return nil, false
	}
	resolver, ok := r.resolvers[t]
	return resolver, ok
}

type RuntimeFactor struct {
	definition   FactorDefinition
	resolver     FactorResolver
	dependencies []FactorCode
}

func (f RuntimeFactor) Code() FactorCode             { return f.definition.Code }
func (f RuntimeFactor) Definition() FactorDefinition { return f.definition }
func (f RuntimeFactor) Dependencies() []FactorCode {
	return append([]FactorCode(nil), f.dependencies...)
}
func (f RuntimeFactor) Resolve(ctx context.Context, req ResolveRequest) (FactorValue, error) {
	return f.resolver.Resolve(ctx, req)
}

type FactorFactory struct {
	resolvers *ResolverRegistry
}

func NewFactorFactory(resolvers *ResolverRegistry) *FactorFactory {
	return &FactorFactory{resolvers: resolvers}
}

func (f *FactorFactory) Create(def FactorDefinition, catalog FactorCatalog) (Factor, error) {
	resolver, ok := f.resolvers.Get(def.Type)
	if !ok {
		return nil, fmt.Errorf("factor %s: resolver not found for type %s", def.Code, def.Type)
	}
	if err := resolver.Validate(def, catalog); err != nil {
		return nil, err
	}
	deps, err := resolver.Dependencies(def, catalog)
	if err != nil {
		return nil, err
	}
	return RuntimeFactor{
		definition:   def,
		resolver:     resolver,
		dependencies: deps,
	}, nil
}

type Registry struct {
	catalog   FactorCatalog
	factors   map[FactorCode]Factor
	factory   *FactorFactory
	resolvers *ResolverRegistry
}

func NewRegistry(definitions []FactorDefinition, resolvers ...FactorResolver) (*Registry, error) {
	resolverRegistry := NewResolverRegistry(resolvers...)
	factory := NewFactorFactory(resolverRegistry)
	catalog := make(FactorCatalog, len(definitions))
	for _, def := range definitions {
		if _, exists := catalog[def.Code]; exists {
			return nil, fmt.Errorf("duplicate factor definition: %s", def.Code)
		}
		catalog[def.Code] = def
	}

	factors := make(map[FactorCode]Factor, len(definitions))
	for _, def := range definitions {
		factor, err := factory.Create(def, catalog)
		if err != nil {
			return nil, err
		}
		factors[def.Code] = factor
	}

	return &Registry{
		catalog:   catalog,
		factors:   factors,
		factory:   factory,
		resolvers: resolverRegistry,
	}, nil
}

func (r *Registry) Catalog() FactorCatalog {
	return r.catalog
}

func (r *Registry) Get(code FactorCode) (Factor, bool) {
	factor, ok := r.factors[code]
	return factor, ok
}
