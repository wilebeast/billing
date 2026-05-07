package factor

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func getByPath(value any, path string) (any, bool, error) {
	if path == "" || path == "$" {
		return value, true, nil
	}

	normalized := strings.TrimPrefix(path, "$.")
	normalized = strings.TrimPrefix(normalized, ".")
	if normalized == "" {
		return value, true, nil
	}

	parts := strings.Split(normalized, ".")
	current := value
	for _, part := range parts {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false, nil
			}
			current = next
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil {
				return nil, false, fmt.Errorf("list path segment %q is not an index", part)
			}
			if idx < 0 || idx >= len(node) {
				return nil, false, nil
			}
			current = node[idx]
		default:
			return nil, false, fmt.Errorf("path %q cannot access %T", path, current)
		}
	}

	return current, true, nil
}

func stableValueText(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(raw)
	}
}
