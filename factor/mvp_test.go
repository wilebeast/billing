package factor

import (
	"context"
	"fmt"
	"math/big"
	"sync/atomic"
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
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	factor, ok := registry.Get("merchant_level")
	if !ok {
		t.Fatalf("runtime factor not found")
	}
	if factor == nil {
		t.Fatalf("runtime factor is nil")
	}
	def, ok := registry.Definition("merchant_level")
	if !ok {
		t.Fatalf("definition not found")
	}
	if def.Type != FactorTypeRPC {
		t.Fatalf("definition type = %s, want RPC", def.Type)
	}
}

func TestCalculationContextBuilderBuildsOrderContext(t *testing.T) {
	builder := NewCalculationContextBuilder()
	ctx := builder.BuildOrder(map[string]any{
		"payment_method": "CARD",
	})

	if ctx.ChargeObject.Scope != FactorScopeOrder {
		t.Fatalf("scope = %s, want ORDER", ctx.ChargeObject.Scope)
	}
	value, ok, err := ctx.GetByScope(FactorScopeOrder, "$.payment_method")
	if err != nil {
		t.Fatalf("GetByScope: %v", err)
	}
	if !ok || value != "CARD" {
		t.Fatalf("value = %#v, ok = %v", value, ok)
	}
}

func TestFactorExecutorResolvesDependencies(t *testing.T) {
	registry, err := NewRegistry([]FactorDefinition{
		{
			Code:     "payment_amount",
			Type:     FactorTypeEventField,
			Scope:    FactorScopeOrder,
			DataType: FactorDataTypeDecimal,
			EventField: &EventFieldConfig{
				SourcePath: "$.payment.amount",
			},
		},
		{
			Code:     "service_fee_rate",
			Type:     FactorTypeConstant,
			Scope:    FactorScopeOrder,
			DataType: FactorDataTypeDecimal,
			Constant: &ConstantConfig{
				Value: "0.20",
			},
		},
		{
			Code:     "service_fee_amount",
			Type:     FactorTypeExpression,
			Scope:    FactorScopeOrder,
			DataType: FactorDataTypeDecimal,
			Expression: &ExpressionConfig{
				Expression: "payment_amount * service_fee_rate",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	executor := NewFactorExecutor(registry, ExecutorDependencies{}, DefaultEngineOptions())
	values, err := executor.ResolveFactors(context.Background(), NewCalculationContext(map[string]any{
		"payment": map[string]any{"amount": 100.0},
	}, ChargeObject{Scope: FactorScopeOrder}), []FactorCode{"service_fee_amount"})
	if err != nil {
		t.Fatalf("ResolveFactors: %v", err)
	}

	got, err := values["service_fee_amount"].AsDecimal()
	if err != nil {
		t.Fatalf("AsDecimal: %v", err)
	}
	if got.Cmp(big.NewRat(20, 1)) != 0 {
		t.Fatalf("service_fee_amount = %s, want 20", got.FloatString(6))
	}
}

func TestFactorExecutorTaskPoolRunsIndependentRPCFactorsConcurrently(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	var current int32
	var maxConcurrent int32

	handler := func(level string) RPCHandler {
		return func(ctx context.Context, request RPCRequest) (RPCResponse, error) {
			active := atomic.AddInt32(&current, 1)
			defer atomic.AddInt32(&current, -1)
			for {
				maxSeen := atomic.LoadInt32(&maxConcurrent)
				if active <= maxSeen {
					break
				}
				if atomic.CompareAndSwapInt32(&maxConcurrent, maxSeen, active) {
					break
				}
			}
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return RPCResponse{}, ctx.Err()
			}
			return RPCResponse{Payload: map[string]any{
				"data": map[string]any{"level": level},
			}}, nil
		}
	}

	rpcProvider := NewInMemoryRPCProvider()
	rpcProvider.Register("GET_LEVEL_A", handler("A"))
	rpcProvider.Register("GET_LEVEL_B", handler("B"))

	engine, err := NewEngineWithOptions([]FactorDefinition{
		{
			Code:     "merchant_level_a",
			Type:     FactorTypeRPC,
			Scope:    FactorScopeOrder,
			DataType: FactorDataTypeString,
			RPC: &RPCConfig{
				ProviderCode: "GET_LEVEL_A",
				Method:       "MerchantService.GetLevelA",
			},
		},
		{
			Code:     "merchant_level_b",
			Type:     FactorTypeRPC,
			Scope:    FactorScopeOrder,
			DataType: FactorDataTypeString,
			RPC: &RPCConfig{
				ProviderCode: "GET_LEVEL_B",
				Method:       "MerchantService.GetLevelB",
			},
		},
	}, rpcProvider, nil, EngineOptions{
		FactorIOWorkers: 2,
		RuleCPUWorkers:  1,
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, resolveErr := engine.Resolve(context.Background(), map[string]any{}, []string{"merchant_level_a", "merchant_level_b"})
		done <- resolveErr
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for RPC tasks to start")
		}
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resolve")
	}

	if got := atomic.LoadInt32(&maxConcurrent); got < 2 {
		t.Fatalf("max concurrency = %d, want at least 2", got)
	}
}

func TestFactorExecutorDAGSchedulerRespectsDependencies(t *testing.T) {
	const testType FactorType = FactorTypeRPC

	started := make(chan string, 3)
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})

	registry, err := NewRegistryWithFactory([]FactorDefinition{
		{Code: "A", Type: testType, Scope: FactorScopeOrder, DataType: FactorDataTypeString},
		{Code: "B", Type: testType, Scope: FactorScopeOrder, DataType: FactorDataTypeString},
		{Code: "C", Type: testType, Scope: FactorScopeOrder, DataType: FactorDataTypeString},
	}, NewFactorFactory(scriptedFactorProvider{
		factorType: testType,
		specs: map[FactorCode]scriptedFactorSpec{
			"A": {value: "a", started: started, release: releaseA},
			"B": {value: "b", started: started, release: releaseB},
			"C": {value: "c", started: started, dependencies: []FactorCode{"A", "B"}},
		},
	}))
	if err != nil {
		t.Fatalf("NewRegistryWithFactory: %v", err)
	}

	executor := NewFactorExecutor(registry, ExecutorDependencies{}, DefaultEngineOptions())
	done := make(chan error, 1)
	go func() {
		_, resolveErr := executor.ResolveFactors(context.Background(), NewCalculationContext(map[string]any{}, ChargeObject{Scope: FactorScopeOrder}), []FactorCode{"C"})
		done <- resolveErr
	}()

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case code := <-started:
			seen[code] = true
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for root DAG nodes to start")
		}
	}
	if !seen["A"] || !seen["B"] {
		t.Fatalf("expected A and B to start first, got %+v", seen)
	}

	select {
	case code := <-started:
		t.Fatalf("unexpected dependent node start before deps complete: %s", code)
	default:
	}

	close(releaseA)
	select {
	case code := <-started:
		t.Fatalf("dependent started before all deps completed: %s", code)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseB)
	select {
	case code := <-started:
		if code != "C" {
			t.Fatalf("expected dependent C to start, got %s", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dependent node to start")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for DAG resolve")
	}
}

func TestFactorExecutorDAGSchedulerDetectsCycle(t *testing.T) {
	const testType FactorType = FactorTypeRPC

	registry, err := NewRegistryWithFactory([]FactorDefinition{
		{Code: "A", Type: testType, Scope: FactorScopeOrder, DataType: FactorDataTypeString},
		{Code: "B", Type: testType, Scope: FactorScopeOrder, DataType: FactorDataTypeString},
	}, NewFactorFactory(scriptedFactorProvider{
		factorType: testType,
		specs: map[FactorCode]scriptedFactorSpec{
			"A": {value: "a", dependencies: []FactorCode{"B"}},
			"B": {value: "b", dependencies: []FactorCode{"A"}},
		},
	}))
	if err != nil {
		t.Fatalf("NewRegistryWithFactory: %v", err)
	}

	executor := NewFactorExecutor(registry, ExecutorDependencies{}, DefaultEngineOptions())
	_, err = executor.ResolveFactors(context.Background(), NewCalculationContext(map[string]any{}, ChargeObject{Scope: FactorScopeOrder}), []FactorCode{"A"})
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	if got := err.Error(); got != "factor dependency cycle detected at A" && got != "factor dependency cycle detected at B" {
		t.Fatalf("unexpected cycle error: %v", err)
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

type scriptedFactorSpec struct {
	value        any
	dependencies []FactorCode
	started      chan<- string
	release      <-chan struct{}
}

type scriptedFactorProvider struct {
	factorType FactorType
	specs      map[FactorCode]scriptedFactorSpec
}

func (p scriptedFactorProvider) Type() FactorType { return p.factorType }

func (p scriptedFactorProvider) NewFactor(def FactorDefinition, _ FactorCatalog) (Factor, error) {
	spec, ok := p.specs[def.Code]
	if !ok {
		return nil, fmt.Errorf("missing scripted factor spec for %s", def.Code)
	}
	return scriptedFactor{definition: def, spec: spec}, nil
}

type scriptedFactor struct {
	definition FactorDefinition
	spec       scriptedFactorSpec
}

func (f scriptedFactor) Definition() FactorDefinition { return f.definition }

func (f scriptedFactor) Dependencies() []FactorCode {
	return append([]FactorCode(nil), f.spec.dependencies...)
}

func (f scriptedFactor) Resolve(ctx context.Context, _ ResolveContext) (FactorValue, error) {
	if f.spec.started != nil {
		f.spec.started <- string(f.definition.Code)
	}
	if f.spec.release != nil {
		select {
		case <-f.spec.release:
		case <-ctx.Done():
			return FactorValue{}, ctx.Err()
		}
	}
	source := FactorSource{
		FactorType: f.definition.Type,
		Scope:      f.definition.Scope,
	}
	return buildFactorValue(f.definition, f.spec.value, source)
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
