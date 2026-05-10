package factor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

type RPCRequest struct {
	ProviderCode string
	Method       string
	Inputs       map[string]FactorValue
}

func (r RPCRequest) MustString(name string) (string, error) {
	value, ok := r.Inputs[name]
	if !ok {
		return "", fmt.Errorf("rpc input %s not found", name)
	}
	raw, ok := value.Value.(string)
	if !ok {
		return "", fmt.Errorf("rpc input %s type = %T, want string", name, value.Value)
	}
	return raw, nil
}

type RPCResponse struct {
	Payload map[string]any
}

type RPCProvider interface {
	Call(ctx context.Context, request RPCRequest) (RPCResponse, error)
}

type TableProvider interface {
	Lookup(ctx context.Context, tableCode string, key map[string]any) (any, error)
}

type RuleTableMatchRequest struct {
	RuleTableCode string
	Inputs        map[string]any
}

type RuleTableMatchResult struct {
	Found         bool
	RowID         string
	OutputValue   any
	Priority      int
	Version       int64
	CostMs        int64
	MatchedByTime time.Time
}

type RuleTableRepository interface {
	Match(ctx context.Context, request RuleTableMatchRequest) (RuleTableMatchResult, error)
}

type RPCHandler func(ctx context.Context, request RPCRequest) (RPCResponse, error)

type InMemoryRPCProvider struct {
	handlers map[string]RPCHandler
}

func NewInMemoryRPCProvider() *InMemoryRPCProvider {
	return &InMemoryRPCProvider{handlers: map[string]RPCHandler{}}
}

func (p *InMemoryRPCProvider) Register(providerCode string, handler RPCHandler) {
	p.handlers[providerCode] = handler
}

func (p *InMemoryRPCProvider) Call(ctx context.Context, request RPCRequest) (RPCResponse, error) {
	handler, ok := p.handlers[request.ProviderCode]
	if !ok {
		return RPCResponse{}, fmt.Errorf("rpc provider %s not found", request.ProviderCode)
	}
	return handler(ctx, request)
}

type InMemoryTableProvider struct {
	tables     map[string]map[string]any
	ruleTables map[string][]RuleTableRow
}

func NewInMemoryTableProvider() *InMemoryTableProvider {
	return &InMemoryTableProvider{
		tables:     map[string]map[string]any{},
		ruleTables: map[string][]RuleTableRow{},
	}
}

func (p *InMemoryTableProvider) Put(tableCode string, key map[string]any, value any) {
	if _, ok := p.tables[tableCode]; !ok {
		p.tables[tableCode] = map[string]any{}
	}
	p.tables[tableCode][serializeKey(key)] = value
}

func (p *InMemoryTableProvider) Lookup(_ context.Context, tableCode string, key map[string]any) (any, error) {
	table, ok := p.tables[tableCode]
	if !ok {
		return nil, fmt.Errorf("table %s not found", tableCode)
	}
	value, ok := table[serializeKey(key)]
	if !ok {
		return nil, fmt.Errorf("table %s key not found", tableCode)
	}
	return value, nil
}

type RuleTableRow struct {
	RowID         string
	Dimensions    map[string]any
	OutputValue   any
	Priority      int
	EffectiveFrom *time.Time
	EffectiveTo   *time.Time
	Version       int64
}

func (p *InMemoryTableProvider) PutRule(tableCode string, row RuleTableRow) {
	p.ruleTables[tableCode] = append(p.ruleTables[tableCode], row)
}

func (p *InMemoryTableProvider) Match(_ context.Context, request RuleTableMatchRequest) (RuleTableMatchResult, error) {
	rows, ok := p.ruleTables[request.RuleTableCode]
	if !ok {
		return RuleTableMatchResult{}, fmt.Errorf("rule table %s not found", request.RuleTableCode)
	}

	effectiveTime, hasTime, err := extractEffectiveTime(request.Inputs)
	if err != nil {
		return RuleTableMatchResult{}, err
	}

	matched := make([]RuleTableRow, 0, len(rows))
	for _, row := range rows {
		if hasTime {
			if row.EffectiveFrom != nil && effectiveTime.Before(*row.EffectiveFrom) {
				continue
			}
			if row.EffectiveTo != nil && !effectiveTime.Before(*row.EffectiveTo) {
				continue
			}
		}
		if !matchDimensions(row.Dimensions, request.Inputs) {
			continue
		}
		matched = append(matched, row)
	}

	if len(matched) == 0 {
		return RuleTableMatchResult{Found: false}, nil
	}

	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Priority != matched[j].Priority {
			return matched[i].Priority > matched[j].Priority
		}
		return matched[i].RowID < matched[j].RowID
	})

	best := matched[0]
	if len(matched) > 1 && matched[1].Priority == best.Priority {
		return RuleTableMatchResult{}, fmt.Errorf(
			"rule table %s conflict: multiple rows matched at priority %d (%s, %s)",
			request.RuleTableCode,
			best.Priority,
			best.RowID,
			matched[1].RowID,
		)
	}

	result := RuleTableMatchResult{
		Found:       true,
		RowID:       best.RowID,
		OutputValue: best.OutputValue,
		Priority:    best.Priority,
		Version:     best.Version,
	}
	if hasTime {
		result.MatchedByTime = effectiveTime
	}
	return result, nil
}

func matchDimensions(dimensions map[string]any, inputs map[string]any) bool {
	for key, expected := range dimensions {
		if stableValueText(expected) == "*" {
			continue
		}
		actual, ok := inputs[key]
		if !ok {
			return false
		}
		if stableValueText(actual) != stableValueText(expected) {
			return false
		}
	}
	return true
}

func extractEffectiveTime(inputs map[string]any) (time.Time, bool, error) {
	for _, key := range []string{"effective_time", "payment_success_time", "biz_time"} {
		raw, ok := inputs[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case time.Time:
			return v, true, nil
		case string:
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return time.Time{}, false, err
			}
			return t, true, nil
		default:
			return time.Time{}, false, fmt.Errorf("effective time %s has unsupported type %T", key, raw)
		}
	}
	return time.Time{}, false, nil
}

func serializeKey(key map[string]any) string {
	keys := make([]string, 0, len(key))
	for k := range key {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, stableValueText(key[k])))
	}
	return strings.Join(parts, "|")
}
