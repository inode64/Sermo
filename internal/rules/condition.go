package rules

import "errors"

// conditionOperator decodes the single operator and operand that form one
// condition node. Callers retain their own policy for recognized operators and
// for presenting malformed nodes.
func conditionOperator(node map[string]any) (string, any, error) {
	switch len(node) {
	case 0:
		return "", nil, errors.New("empty condition")
	case 1:
		for operator, operand := range node {
			return operator, operand, nil
		}
		return "", nil, errors.New("empty condition")
	default:
		return "", nil, errors.New("condition must contain exactly one operator")
	}
}

// WalkConditionLeaves visits every leaf in a generic condition tree. The
// visitor returns true to stop the walk; WalkConditionLeaves reports whether it
// stopped early. Logical branches are traversed structurally, while malformed
// operands are ignored so read-only discovery remains safe on partially built
// trees. Validation and evaluation retain their own stricter semantics.
func WalkConditionLeaves(node any, visit func(operator string, operand any) bool) bool {
	m, ok := node.(map[string]any)
	if !ok {
		return false
	}
	for operator, operand := range m {
		switch operator {
		case ConditionAnd, ConditionOr:
			children, ok := operand.([]any)
			if !ok {
				continue
			}
			for _, child := range children {
				if WalkConditionLeaves(child, visit) {
					return true
				}
			}
		case ConditionNot:
			if WalkConditionLeaves(operand, visit) {
				return true
			}
		default:
			if visit(operator, operand) {
				return true
			}
		}
	}
	return false
}
