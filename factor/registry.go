package factor

import "fmt"

type FactorFactory struct {
	providers map[FactorType]FactorProvider
}

func NewFactorFactory(providers ...FactorProvider) *FactorFactory {
	factory := &FactorFactory{providers: map[FactorType]FactorProvider{}}
	for _, provider := range providers {
		factory.Register(provider)
	}
	return factory
}

func (f *FactorFactory) Register(provider FactorProvider) {
	if f.providers == nil {
		f.providers = map[FactorType]FactorProvider{}
	}
	f.providers[provider.Type()] = provider
}

func (f *FactorFactory) Create(def FactorDefinition) (Factor, error) {
	provider, ok := f.providers[def.Type]
	if !ok {
		return nil, fmt.Errorf("factor %s: provider not found for type %s", def.Code, def.Type)
	}
	return provider.NewFactor(def)
}

type Registry struct {
	catalog FactorCatalog
	factors map[FactorCode]Factor
	factory *FactorFactory
}

func NewRegistry(definitions []FactorDefinition) (*Registry, error) {
	return NewRegistryWithFactory(definitions, NewFactorFactory(
		EventFieldFactorProvider{},
		ExpressionFactorProvider{},
		RPCFactorProvider{},
		TableLookupFactorProvider{},
		RuleTableFactorProvider{},
		RateMatchFactorProvider{},
		ConstantFactorProvider{},
	))
}

func NewRegistryWithFactory(definitions []FactorDefinition, factory *FactorFactory) (*Registry, error) {
	catalog := make(FactorCatalog, len(definitions))
	for _, def := range definitions {
		if _, exists := catalog[def.Code]; exists {
			return nil, fmt.Errorf("duplicate factor definition: %s", def.Code)
		}
		catalog[def.Code] = def
	}

	factors := make(map[FactorCode]Factor, len(definitions))
	for _, def := range definitions {
		factor, err := factory.Create(def)
		if err != nil {
			return nil, err
		}
		factors[def.Code] = factor
	}

	return &Registry{
		catalog: catalog,
		factors: factors,
		factory: factory,
	}, nil
}

func (r *Registry) Catalog() FactorCatalog {
	return r.catalog
}

func (r *Registry) Definition(code FactorCode) (FactorDefinition, bool) {
	def, ok := r.catalog[code]
	return def, ok
}

func (r *Registry) Get(code FactorCode) (Factor, bool) {
	factor, ok := r.factors[code]
	return factor, ok
}
