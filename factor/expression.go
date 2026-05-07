package factor

import (
	"fmt"
	"sort"

	"github.com/Knetic/govaluate"
)

func expressionVariables(expression string) ([]string, error) {
	expr, err := govaluate.NewEvaluableExpression(expression)
	if err != nil {
		return nil, err
	}
	vars := expr.Vars()
	sort.Strings(vars)
	return vars, nil
}

func evaluateExpression(expression string, params map[string]any) (any, error) {
	expr, err := govaluate.NewEvaluableExpression(expression)
	if err != nil {
		return nil, err
	}

	result, err := expr.Evaluate(params)
	if err != nil {
		return nil, fmt.Errorf("evaluate expression %q: %w", expression, err)
	}
	return result, nil
}
