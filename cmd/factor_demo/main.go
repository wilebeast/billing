package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"billing/factor"
)

func main() {
	defs := []factor.FactorDefinition{
		{
			Code:     "payment_amount",
			Type:     factor.FactorTypeEventField,
			DataType: factor.FactorDataTypeDecimal,
			Required: true,
			EventField: &factor.EventFieldConfig{
				SourcePath: "$.payment.amount",
			},
		},
		{
			Code:     "merchant_id",
			Type:     factor.FactorTypeEventField,
			DataType: factor.FactorDataTypeString,
			Required: true,
			EventField: &factor.EventFieldConfig{
				SourcePath: "$.merchant.id",
			},
		},
		{
			Code:     "merchant_level",
			Type:     factor.FactorTypeRPC,
			DataType: factor.FactorDataTypeString,
			RPC: &factor.RPCConfig{
				ProviderCode: "GET_MERCHANT_LEVEL",
				Method:       "MerchantService.GetMerchantLevel",
				InputMapping: map[string]string{"merchant_id": "merchant_id"},
				OutputPath:   "$.data.level",
			},
		},
		{
			Code:     "service_fee_rate",
			Type:     factor.FactorTypeTable,
			DataType: factor.FactorDataTypeDecimal,
			Table: &factor.TableLookupConfig{
				TableCode:  "service_fee_rate",
				LookupKey:  map[string]string{"merchant_level": "merchant_level"},
				OutputPath: "$.rate",
			},
		},
		{
			Code:     "service_fee_amount",
			Type:     factor.FactorTypeExpression,
			DataType: factor.FactorDataTypeDecimal,
			Expression: &factor.ExpressionConfig{
				Expression:    "payment_amount * service_fee_rate",
				DependFactors: []string{"payment_amount", "service_fee_rate"},
			},
		},
	}

	rpcProvider := factor.NewInMemoryRPCProvider()
	rpcProvider.Register("GET_MERCHANT_LEVEL", func(_ context.Context, _ string, input map[string]any) (any, error) {
		level := "NORMAL"
		if input["merchant_id"] == "m_vip" {
			level = "VIP"
		}
		return map[string]any{"data": map[string]any{"level": level}}, nil
	})

	tableProvider := factor.NewInMemoryTableProvider()
	tableProvider.Put("service_fee_rate", map[string]any{"merchant_level": "NORMAL"}, map[string]any{"rate": 0.2})
	tableProvider.Put("service_fee_rate", map[string]any{"merchant_level": "VIP"}, map[string]any{"rate": 0.15})

	engine, err := factor.NewEngine(defs, rpcProvider, tableProvider)
	if err != nil {
		log.Fatal(err)
	}

	event := map[string]any{
		"payment":  map[string]any{"amount": 100.0},
		"merchant": map[string]any{"id": "m_001"},
	}

	result, err := engine.Resolve(context.Background(), event, []string{"service_fee_amount"})
	if err != nil {
		log.Fatal(err)
	}

	raw, err := json.MarshalIndent(result.Factors, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(raw))
}
