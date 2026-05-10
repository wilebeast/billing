package factor

import (
	"context"
	"math/big"
	"testing"
	"time"
)

func strptr(s string) *string { return &s }

func TestResolveMVPFactors(t *testing.T) {
	engine, err := NewEngine([]FactorDefinition{
		{
			Code:     "payment_amount",
			Type:     FactorTypeEventField,
			DataType: FactorDataTypeDecimal,
			Required: true,
			EventField: &EventFieldConfig{
				SourcePath: "$.payment.amount",
			},
		},
		{
			Code:         "discount_amount",
			Type:         FactorTypeEventField,
			DataType:     FactorDataTypeDecimal,
			Required:     false,
			DefaultValue: strptr("0"),
			EventField: &EventFieldConfig{
				SourcePath: "$.payment.discount_amount",
			},
		},
		{
			Code:     "merchant_id",
			Type:     FactorTypeEventField,
			DataType: FactorDataTypeString,
			Required: true,
			EventField: &EventFieldConfig{
				SourcePath: "$.merchant.id",
			},
		},
		{
			Code:     "base_amount",
			Type:     FactorTypeExpression,
			DataType: FactorDataTypeDecimal,
			Expression: &ExpressionConfig{
				Expression:    "payment_amount - discount_amount",
				DependFactors: []string{"payment_amount", "discount_amount"},
			},
		},
		{
			Code:     "merchant_level",
			Type:     FactorTypeRPC,
			DataType: FactorDataTypeString,
			RPC: &RPCConfig{
				ProviderCode: "GET_MERCHANT_LEVEL",
				Method:       "MerchantService.GetMerchantLevel",
				InputMapping: map[string]string{
					"merchant_id": "merchant_id",
				},
				OutputPath: "$.data.level",
			},
		},
		{
			Code:     "service_fee_rate",
			Type:     FactorTypeTable,
			DataType: FactorDataTypeDecimal,
			Table: &TableLookupConfig{
				TableCode: "service_fee_rate",
				LookupKey: map[string]string{
					"merchant_level": "merchant_level",
				},
				OutputPath: "$.rate",
			},
		},
		{
			Code:     "service_fee_amount",
			Type:     FactorTypeExpression,
			DataType: FactorDataTypeDecimal,
			Expression: &ExpressionConfig{
				Expression:    "base_amount * service_fee_rate",
				DependFactors: []string{"base_amount", "service_fee_rate"},
			},
		},
	}, makeRPCProvider(), makeTableProvider())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	event := map[string]any{
		"payment": map[string]any{
			"amount": 100.0,
		},
		"merchant": map[string]any{
			"id": "m_001",
		},
	}

	ctx, err := engine.Resolve(context.Background(), event, []string{"service_fee_amount"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got := ctx.Factors["discount_amount"].Status; got != FactorStatusDefaulted {
		t.Fatalf("discount_amount status = %s, want %s", got, FactorStatusDefaulted)
	}

	if got := ctx.Factors["merchant_level"].Value.(string); got != "NORMAL" {
		t.Fatalf("merchant_level = %s", got)
	}

	got, err := ctx.Factors["service_fee_amount"].AsDecimal()
	if err != nil {
		t.Fatalf("service_fee_amount AsDecimal: %v", err)
	}
	if got.Cmp(big.NewRat(20, 1)) != 0 {
		t.Fatalf("service_fee_amount = %s, want 20", got.FloatString(6))
	}
}

func TestGetByPathListIndex(t *testing.T) {
	event := map[string]any{
		"items": []any{
			map[string]any{
				"sku":    "sku_001",
				"amount": 10.0,
			},
			map[string]any{
				"sku":    "sku_002",
				"amount": 20.0,
			},
		},
	}

	value, ok, err := getByPath(event, "$.items.1.amount")
	if err != nil {
		t.Fatalf("getByPath returned error: %v", err)
	}
	if !ok {
		t.Fatalf("getByPath did not find list element")
	}

	got, ok := value.(float64)
	if !ok {
		t.Fatalf("value type = %T, want float64", value)
	}
	if got != 20.0 {
		t.Fatalf("value = %v, want 20", got)
	}
}

func TestRegistryBuildsRuntimeFactors(t *testing.T) {
	registry, err := NewRegistry([]FactorDefinition{
		{
			Code:     "merchant_id",
			Type:     FactorTypeEventField,
			Scope:    FactorScopeOrder,
			DataType: FactorDataTypeString,
			EventField: &EventFieldConfig{
				SourcePath: "$.merchant.id",
			},
		},
		{
			Code:     "merchant_level",
			Type:     FactorTypeRPC,
			Scope:    FactorScopeOrder,
			DataType: FactorDataTypeString,
			RPC: &RPCConfig{
				ProviderCode: "GET_MERCHANT_LEVEL",
				Method:       "MerchantService.GetMerchantLevel",
				InputMapping: map[string]string{"merchant_id": "merchant_id"},
				OutputPath:   "$.data.level",
			},
		},
	}, EventFieldResolver{}, RPCResolver{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	factor, ok := registry.Get("merchant_level")
	if !ok {
		t.Fatalf("runtime factor not found")
	}
	deps := factor.Dependencies()
	if len(deps) != 1 || deps[0] != "merchant_id" {
		t.Fatalf("deps = %v, want [merchant_id]", deps)
	}
}

func TestRuleTableFactorAndSnapshots(t *testing.T) {
	engine, err := NewEngine([]FactorDefinition{
		{
			Code:     "category_id",
			Type:     FactorTypeEventField,
			Scope:    FactorScopeItem,
			DataType: FactorDataTypeString,
			EventField: &EventFieldConfig{
				SourcePath: "$.category_id",
			},
		},
		{
			Code:     "service_fee_rate",
			Type:     FactorTypeRuleTable,
			Scope:    FactorScopeItem,
			DataType: FactorDataTypeDecimal,
			RuleTable: &RuleTableConfig{
				RuleTableCode: "service_fee_rate_match",
				InputMapping:  map[string]string{"category_id": "category_id"},
				OutputPath:    "$.rate",
			},
		},
	}, makeRPCProvider(), makeRuleTableProvider())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	ctx, err := engine.ResolveWithContext(
		context.Background(),
		NewCalculationContext(
			map[string]any{},
			ChargeObject{
				Scope:   FactorScopeItem,
				ID:      "I001",
				Payload: map[string]any{"category_id": "Electronics"},
			},
		),
		[]string{"service_fee_rate"},
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	value := ctx.Factors["service_fee_rate"]
	if value.Source.RuleTableCode != "service_fee_rate_match" {
		t.Fatalf("rule table source = %s", value.Source.RuleTableCode)
	}
	snapshot := value.Snapshot()
	if snapshot.Value != "0.120000000000" {
		t.Fatalf("snapshot value = %#v", snapshot.Value)
	}
}

func TestResolveWithCalculationContextUsesItemScope(t *testing.T) {
	engine, err := NewEngine([]FactorDefinition{
		{
			Code:     "payment_method",
			Type:     FactorTypeEventField,
			Scope:    FactorScopeOrder,
			DataType: FactorDataTypeString,
			EventField: &EventFieldConfig{
				SourcePath: "$.payment_method",
			},
		},
		{
			Code:     "category_id",
			Type:     FactorTypeEventField,
			Scope:    FactorScopeItem,
			DataType: FactorDataTypeString,
			EventField: &EventFieldConfig{
				SourcePath: "$.category_id",
			},
		},
		{
			Code:     "rate",
			Type:     FactorTypeRateMatch,
			Scope:    FactorScopeItem,
			DataType: FactorDataTypeDecimal,
			RuleTable: &RuleTableConfig{
				RuleTableCode: "category_rate",
				InputMapping: map[string]string{
					"payment_method": "payment_method",
					"category_id":    "category_id",
				},
			},
		},
	}, makeRPCProvider(), makeCategoryRateProvider())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	calcCtx := NewCalculationContext(
		map[string]any{"payment_method": "CARD"},
		ChargeObject{
			Scope:   FactorScopeItem,
			ID:      "I001",
			Payload: map[string]any{"category_id": "Electronics"},
		},
	)

	ctx, err := engine.ResolveWithContext(context.Background(), calcCtx, []string{"rate"})
	if err != nil {
		t.Fatalf("resolve with context: %v", err)
	}

	got, err := ctx.Factors["rate"].AsDecimal()
	if err != nil {
		t.Fatalf("rate AsDecimal: %v", err)
	}
	if got.Cmp(big.NewRat(12, 100)) != 0 {
		t.Fatalf("rate = %s, want 0.12", got.FloatString(2))
	}
}

func TestRateMatchConflictReturnsFailedFactor(t *testing.T) {
	engine, err := NewEngine([]FactorDefinition{
		{
			Code:     "category_id",
			Type:     FactorTypeEventField,
			Scope:    FactorScopeItem,
			DataType: FactorDataTypeString,
			EventField: &EventFieldConfig{
				SourcePath: "$.category_id",
			},
		},
		{
			Code:     "payment_success_time",
			Type:     FactorTypeEventField,
			Scope:    FactorScopeOrder,
			DataType: FactorDataTypeDatetime,
			EventField: &EventFieldConfig{
				SourcePath: "$.payment_success_time",
			},
		},
		{
			Code:     "rate",
			Type:     FactorTypeRateMatch,
			Scope:    FactorScopeItem,
			DataType: FactorDataTypeDecimal,
			RuleTable: &RuleTableConfig{
				RuleTableCode: "conflict_rate",
				InputMapping: map[string]string{
					"category_id":          "category_id",
					"payment_success_time": "payment_success_time",
				},
			},
		},
	}, makeRPCProvider(), makeConflictRateProvider())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	calcCtx := NewCalculationContext(
		map[string]any{"payment_success_time": "2026-05-15T10:30:00Z"},
		ChargeObject{
			Scope:   FactorScopeItem,
			ID:      "I001",
			Payload: map[string]any{"category_id": "Electronics"},
		},
	)

	ctx, err := engine.ResolveWithContext(context.Background(), calcCtx, []string{"rate"})
	if err != nil {
		t.Fatalf("resolve with context: %v", err)
	}

	if got := ctx.Factors["rate"].Status; got != FactorStatusFailed {
		t.Fatalf("rate status = %s, want FAILED", got)
	}
	if got := ctx.Factors["rate"].ErrorCode; got != "RULE_TABLE_CONFLICT" {
		t.Fatalf("rate error code = %s", got)
	}
}

func makeRPCProvider() *InMemoryRPCProvider {
	provider := NewInMemoryRPCProvider()
	provider.Register("GET_MERCHANT_LEVEL", func(_ context.Context, request RPCRequest) (RPCResponse, error) {
		if request.Method != "MerchantService.GetMerchantLevel" {
			return RPCResponse{}, nil
		}
		merchantID, err := request.MustString("merchant_id")
		if err != nil {
			return RPCResponse{}, err
		}
		level := "NORMAL"
		if merchantID == "m_vip" {
			level = "VIP"
		}
		return RPCResponse{Payload: map[string]any{
			"data": map[string]any{
				"level": level,
			},
		}}, nil
	})
	return provider
}

func makeTableProvider() *InMemoryTableProvider {
	table := NewInMemoryTableProvider()
	table.Put("service_fee_rate", map[string]any{"merchant_level": "NORMAL"}, map[string]any{"rate": 0.2})
	table.Put("service_fee_rate", map[string]any{"merchant_level": "VIP"}, map[string]any{"rate": 0.15})
	return table
}

func makeRuleTableProvider() *InMemoryTableProvider {
	table := NewInMemoryTableProvider()
	table.PutRule("service_fee_rate_match", RuleTableRow{
		RowID:       "RR_ELECTRONICS",
		Dimensions:  map[string]any{"category_id": "Electronics"},
		OutputValue: map[string]any{"rate": "0.12"},
		Priority:    10,
		Version:     1,
	})
	table.PutRule("service_fee_rate_match", RuleTableRow{
		RowID:       "RR_FASHION",
		Dimensions:  map[string]any{"category_id": "Fashion"},
		OutputValue: map[string]any{"rate": "0.18"},
		Priority:    10,
		Version:     1,
	})
	return table
}

func makeCategoryRateProvider() *InMemoryTableProvider {
	table := NewInMemoryTableProvider()
	table.PutRule("category_rate", RuleTableRow{
		RowID:       "RR_CARD_ELECTRONICS",
		Dimensions:  map[string]any{"payment_method": "CARD", "category_id": "Electronics"},
		OutputValue: "0.12",
		Priority:    10,
		Version:     1,
	})
	table.PutRule("category_rate", RuleTableRow{
		RowID:       "RR_DEFAULT",
		Dimensions:  map[string]any{"payment_method": "*", "category_id": "*"},
		OutputValue: "0.25",
		Priority:    1,
		Version:     1,
	})
	return table
}

func makeConflictRateProvider() *InMemoryTableProvider {
	table := NewInMemoryTableProvider()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	table.PutRule("conflict_rate", RuleTableRow{
		RowID:         "RR_A",
		Dimensions:    map[string]any{"category_id": "Electronics"},
		OutputValue:   "0.12",
		Priority:      10,
		EffectiveFrom: &from,
		Version:       1,
	})
	table.PutRule("conflict_rate", RuleTableRow{
		RowID:         "RR_B",
		Dimensions:    map[string]any{"category_id": "Electronics"},
		OutputValue:   "0.13",
		Priority:      10,
		EffectiveFrom: &from,
		Version:       1,
	})
	return table
}
