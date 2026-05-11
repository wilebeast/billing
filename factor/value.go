package factor

import (
	"fmt"
	"math/big"
	"strconv"
	"sync"
	"time"
)

type FactorSource struct {
	FactorType    FactorType     `json:"factor_type"`
	Scope         FactorScope    `json:"scope,omitempty"`
	SourcePath    string         `json:"source_path,omitempty"`
	ProviderCode  string         `json:"provider_code,omitempty"`
	Method        string         `json:"method,omitempty"`
	Request       map[string]any `json:"request,omitempty"`
	ResponsePath  string         `json:"response_path,omitempty"`
	TableCode     string         `json:"table_code,omitempty"`
	LookupKey     map[string]any `json:"lookup_key,omitempty"`
	RuleTableCode string         `json:"rule_table_code,omitempty"`
	MatchedRowID  string         `json:"matched_row_id,omitempty"`
	MatchedByTime string         `json:"matched_by_time,omitempty"`
	MatchedInputs map[string]any `json:"matched_inputs,omitempty"`
	Expression    string         `json:"expression,omitempty"`
	ConstantValue any            `json:"constant_value,omitempty"`
	Priority      int            `json:"priority,omitempty"`
	Version       int64          `json:"version,omitempty"`
	CostMs        int64          `json:"cost_ms,omitempty"`
}

type FactorValue struct {
	Code         FactorCode     `json:"code"`
	FactorType   FactorType     `json:"factor_type"`
	DataType     FactorDataType `json:"data_type"`
	Status       FactorStatus   `json:"status"`
	Value        any            `json:"-"`
	RawValue     any            `json:"-"`
	Source       FactorSource   `json:"source"`
	ErrorCode    string         `json:"error_code,omitempty"`
	ErrorMessage string         `json:"error_message,omitempty"`
	Extra        map[string]any `json:"extra,omitempty"`
	ValueText    string         `json:"value_text,omitempty"`
}

type FactorValueSnapshot struct {
	Code         FactorCode     `json:"code"`
	FactorType   FactorType     `json:"factor_type"`
	DataType     FactorDataType `json:"data_type"`
	Status       FactorStatus   `json:"status"`
	Value        any            `json:"value,omitempty"`
	RawValue     any            `json:"raw_value,omitempty"`
	Source       FactorSource   `json:"source"`
	ErrorCode    string         `json:"error_code,omitempty"`
	ErrorMessage string         `json:"error_message,omitempty"`
	Extra        map[string]any `json:"extra,omitempty"`
}

func (v FactorValue) IsOK() bool {
	return v.Status == FactorStatusOK || v.Status == FactorStatusDefaulted
}

func (v FactorValue) AsDecimal() (*big.Rat, error) {
	if !v.IsOK() {
		return nil, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
	}
	if v.DataType != FactorDataTypeDecimal {
		return nil, fmt.Errorf("factor %s is not DECIMAL, got %s", v.Code, v.DataType)
	}
	rat, ok := v.Value.(*big.Rat)
	if !ok || rat == nil {
		return nil, fmt.Errorf("factor %s value type mismatch, expected *big.Rat", v.Code)
	}
	return new(big.Rat).Set(rat), nil
}

func (v FactorValue) AsString() (string, error) {
	if !v.IsOK() {
		return "", fmt.Errorf("factor %s status is %s", v.Code, v.Status)
	}
	if v.DataType != FactorDataTypeString {
		return "", fmt.Errorf("factor %s is not STRING, got %s", v.Code, v.DataType)
	}
	s, ok := v.Value.(string)
	if !ok {
		return "", fmt.Errorf("factor %s value type mismatch, expected string", v.Code)
	}
	return s, nil
}

func (v FactorValue) AsInt64() (int64, error) {
	if !v.IsOK() {
		return 0, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
	}
	if v.DataType != FactorDataTypeInt64 {
		return 0, fmt.Errorf("factor %s is not INT64, got %s", v.Code, v.DataType)
	}
	n, ok := v.Value.(int64)
	if !ok {
		return 0, fmt.Errorf("factor %s value type mismatch, expected int64", v.Code)
	}
	return n, nil
}

func (v FactorValue) AsBool() (bool, error) {
	if !v.IsOK() {
		return false, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
	}
	if v.DataType != FactorDataTypeBool {
		return false, fmt.Errorf("factor %s is not BOOL, got %s", v.Code, v.DataType)
	}
	b, ok := v.Value.(bool)
	if !ok {
		return false, fmt.Errorf("factor %s value type mismatch, expected bool", v.Code)
	}
	return b, nil
}

func (v FactorValue) AsTime() (time.Time, error) {
	if !v.IsOK() {
		return time.Time{}, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
	}
	if v.DataType != FactorDataTypeDatetime {
		return time.Time{}, fmt.Errorf("factor %s is not DATETIME, got %s", v.Code, v.DataType)
	}
	t, ok := v.Value.(time.Time)
	if !ok {
		return time.Time{}, fmt.Errorf("factor %s value type mismatch, expected time.Time", v.Code)
	}
	return t, nil
}

func (v FactorValue) ToExpressionParam() (any, error) {
	switch v.DataType {
	case FactorDataTypeDecimal:
		rat, err := v.AsDecimal()
		if err != nil {
			return nil, err
		}
		f, _ := rat.Float64()
		return f, nil
	case FactorDataTypeString:
		return v.AsString()
	case FactorDataTypeInt64:
		return v.AsInt64()
	case FactorDataTypeBool:
		return v.AsBool()
	case FactorDataTypeDatetime:
		return v.AsTime()
	case FactorDataTypeObject, FactorDataTypeArray:
		if !v.IsOK() {
			return nil, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
		}
		return v.Value, nil
	default:
		if !v.IsOK() {
			return nil, fmt.Errorf("factor %s status is %s", v.Code, v.Status)
		}
		return v.Value, nil
	}
}

func (v FactorValue) SnapshotValue() any {
	switch typed := v.Value.(type) {
	case *big.Rat:
		if typed == nil {
			return nil
		}
		return typed.FloatString(12)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		return typed
	}
}

func (v FactorValue) Snapshot() FactorValueSnapshot {
	return FactorValueSnapshot{
		Code:         v.Code,
		FactorType:   v.FactorType,
		DataType:     v.DataType,
		Status:       v.Status,
		Value:        v.SnapshotValue(),
		RawValue:     v.RawValue,
		Source:       v.Source,
		ErrorCode:    v.ErrorCode,
		ErrorMessage: v.ErrorMessage,
		Extra:        v.Extra,
	}
}

func NewOKFactorValue(code FactorCode, dataType FactorDataType, value any, rawValue any, source FactorSource) FactorValue {
	return FactorValue{
		Code:       code,
		FactorType: source.FactorType,
		DataType:   dataType,
		Status:     FactorStatusOK,
		Value:      value,
		RawValue:   rawValue,
		Source:     source,
		ValueText:  stableValueText(value),
	}
}

func NewMissingFactorValue(code FactorCode, dataType FactorDataType, source FactorSource) FactorValue {
	return FactorValue{
		Code:         code,
		FactorType:   source.FactorType,
		DataType:     dataType,
		Status:       FactorStatusMissing,
		Source:       source,
		ErrorCode:    "FACTOR_MISSING",
		ErrorMessage: "factor value is missing",
	}
}

func NewFailedFactorValue(code FactorCode, dataType FactorDataType, source FactorSource, errorCode string, errorMessage string) FactorValue {
	return FactorValue{
		Code:         code,
		FactorType:   source.FactorType,
		DataType:     dataType,
		Status:       FactorStatusFailed,
		Source:       source,
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	}
}

func NewInvalidFactorValue(code FactorCode, dataType FactorDataType, rawValue any, source FactorSource, errorMessage string) FactorValue {
	return FactorValue{
		Code:         code,
		FactorType:   source.FactorType,
		DataType:     dataType,
		Status:       FactorStatusInvalid,
		RawValue:     rawValue,
		Source:       source,
		ErrorCode:    "FACTOR_INVALID",
		ErrorMessage: errorMessage,
	}
}

func NewNullFactorValue(code FactorCode, dataType FactorDataType, source FactorSource) FactorValue {
	return FactorValue{
		Code:       code,
		FactorType: source.FactorType,
		DataType:   dataType,
		Status:     FactorStatusNull,
		RawValue:   nil,
		Source:     source,
		ValueText:  "null",
	}
}

func NewDefaultedFactorValue(code FactorCode, factorType FactorType, dataType FactorDataType, value any, rawValue any, source FactorSource) FactorValue {
	return FactorValue{
		Code:       code,
		FactorType: factorType,
		DataType:   dataType,
		Status:     FactorStatusDefaulted,
		Value:      value,
		RawValue:   rawValue,
		Source:     source,
		ValueText:  stableValueText(value),
	}
}

func NormalizeRawValue(raw any, dataType FactorDataType) (any, error) {
	switch dataType {
	case FactorDataTypeDecimal:
		return normalizeDecimal(raw)
	case FactorDataTypeString:
		return normalizeString(raw)
	case FactorDataTypeInt64:
		return normalizeInt64(raw)
	case FactorDataTypeBool:
		return normalizeBool(raw)
	case FactorDataTypeDatetime:
		return normalizeTime(raw)
	case FactorDataTypeObject:
		return raw, nil
	case FactorDataTypeArray:
		return raw, nil
	default:
		return nil, fmt.Errorf("unsupported data type: %s", dataType)
	}
}

func normalizeDecimal(raw any) (*big.Rat, error) {
	switch v := raw.(type) {
	case *big.Rat:
		if v == nil {
			return nil, fmt.Errorf("nil decimal")
		}
		return new(big.Rat).Set(v), nil
	case string:
		r, ok := new(big.Rat).SetString(v)
		if !ok {
			return nil, fmt.Errorf("invalid decimal string %q", v)
		}
		return r, nil
	case int:
		return new(big.Rat).SetInt64(int64(v)), nil
	case int64:
		return new(big.Rat).SetInt64(v), nil
	case float64:
		s := strconv.FormatFloat(v, 'f', -1, 64)
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return nil, fmt.Errorf("invalid decimal float %v", v)
		}
		return r, nil
	default:
		return nil, fmt.Errorf("unsupported decimal source %T", raw)
	}
}

func normalizeString(raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		return v, nil
	default:
		return stableValueText(v), nil
	}
}

func normalizeInt64(raw any) (int64, error) {
	switch v := raw.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		return int64(v), nil
	case string:
		i, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, err
		}
		return i, nil
	default:
		return 0, fmt.Errorf("unsupported int64 source %T", raw)
	}
}

func normalizeBool(raw any) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		return strconv.ParseBool(v)
	default:
		return false, fmt.Errorf("unsupported bool source %T", raw)
	}
}

func normalizeTime(raw any) (time.Time, error) {
	switch v := raw.(type) {
	case time.Time:
		return v, nil
	case string:
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, err
		}
		return t, nil
	default:
		return time.Time{}, fmt.Errorf("unsupported datetime source %T", raw)
	}
}

type FactorValueStore interface {
	Get(key FactorInstanceKey) (FactorValue, bool)
	Set(key FactorInstanceKey, value FactorValue)
	All() map[FactorInstanceKey]FactorValue
}

type InMemoryFactorValueStore struct {
	mu     sync.RWMutex
	values map[FactorInstanceKey]FactorValue
}

func NewInMemoryFactorValueStore() *InMemoryFactorValueStore {
	return &InMemoryFactorValueStore{values: map[FactorInstanceKey]FactorValue{}}
}

func (s *InMemoryFactorValueStore) Get(key FactorInstanceKey) (FactorValue, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

func (s *InMemoryFactorValueStore) Set(key FactorInstanceKey, value FactorValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
}

func (s *InMemoryFactorValueStore) All() map[FactorInstanceKey]FactorValue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[FactorInstanceKey]FactorValue, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out
}
