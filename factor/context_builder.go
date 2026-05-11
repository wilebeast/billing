package factor

type CalculationContextBuilder struct{}

func NewCalculationContextBuilder() CalculationContextBuilder {
	return CalculationContextBuilder{}
}

func (b CalculationContextBuilder) BuildOrder(event map[string]any) CalculationContext {
	return NewCalculationContext(event, ChargeObject{
		Scope:   FactorScopeOrder,
		Payload: nil,
	})
}

func (b CalculationContextBuilder) Build(event map[string]any, object ChargeObject) CalculationContext {
	return NewCalculationContext(event, object)
}
