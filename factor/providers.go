package factor

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	tables map[string]map[string]any
}

func NewInMemoryTableProvider() *InMemoryTableProvider {
	return &InMemoryTableProvider{tables: map[string]map[string]any{}}
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
