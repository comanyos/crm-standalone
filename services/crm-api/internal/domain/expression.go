package domain

// A deliberately small expression evaluator for tenant-authored formulas and
// validation rules. It uses Go's parser only as a lexer/parser; evaluation
// accepts a strict, non-Turing-complete AST subset. No calls, selectors,
// indexing, assignment, loops, or recursion in the authored language exist.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"time"
)

const (
	maxExpressionBytes = 4096
	maxExpressionSteps = 512
	maxExpressionDepth = 32
)

// EvaluateExpression evaluates a formula against the already-authorized
// record values. It has neither store nor context access, which makes a
// cross-tenant read structurally impossible.
func EvaluateExpression(source string, values CustomValues) (any, error) {
	if len(source) == 0 || len(source) > maxExpressionBytes {
		return nil, fmt.Errorf("%w: expression must be 1..%d bytes", ErrInvalid, maxExpressionBytes)
	}
	expr, err := parser.ParseExpr(source)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid expression", ErrInvalid)
	}
	state := expressionState{values: values, deadline: time.Now().Add(10 * time.Millisecond)}
	return state.eval(expr, 0)
}

func ValidateExpression(source string) error {
	if len(source) == 0 || len(source) > maxExpressionBytes {
		return fmt.Errorf("%w: expression must be 1..%d bytes", ErrInvalid, maxExpressionBytes)
	}
	expr, err := parser.ParseExpr(source)
	if err != nil {
		return fmt.Errorf("%w: invalid expression", ErrInvalid)
	}
	return validateExpressionNode(expr, 0)
}

func validateExpressionNode(node ast.Expr, depth int) error {
	if depth > maxExpressionDepth {
		return fmt.Errorf("%w: expression is too deeply nested", ErrInvalid)
	}
	switch n := node.(type) {
	case *ast.Ident:
		return nil
	case *ast.BasicLit:
		if n.Kind == token.INT || n.Kind == token.FLOAT || n.Kind == token.STRING {
			return nil
		}
	case *ast.ParenExpr:
		return validateExpressionNode(n.X, depth+1)
	case *ast.UnaryExpr:
		if n.Op == token.NOT || n.Op == token.ADD || n.Op == token.SUB {
			return validateExpressionNode(n.X, depth+1)
		}
	case *ast.BinaryExpr:
		switch n.Op {
		case token.ADD, token.SUB, token.MUL, token.QUO, token.REM, token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
			if err := validateExpressionNode(n.X, depth+1); err != nil {
				return err
			}
			return validateExpressionNode(n.Y, depth+1)
		}
	}
	return fmt.Errorf("%w: expression uses unsupported syntax", ErrInvalid)
}

type expressionState struct {
	values   CustomValues
	steps    int
	deadline time.Time
}

func (s *expressionState) eval(node ast.Expr, depth int) (any, error) {
	s.steps++
	if s.steps > maxExpressionSteps || depth > maxExpressionDepth || time.Now().After(s.deadline) {
		return nil, fmt.Errorf("%w: expression exceeded evaluation budget", ErrInvalid)
	}
	switch n := node.(type) {
	case *ast.ParenExpr:
		return s.eval(n.X, depth+1)
	case *ast.Ident:
		if n.Name == "true" {
			return true, nil
		}
		if n.Name == "false" {
			return false, nil
		}
		if s.values == nil {
			return float64(0), nil
		}
		v, ok := s.values[n.Name]
		if !ok {
			return nil, fmt.Errorf("%w: expression references unknown field", ErrInvalid)
		}
		return v, nil
	case *ast.BasicLit:
		switch n.Kind {
		case token.INT, token.FLOAT:
			v, err := strconv.ParseFloat(n.Value, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid number", ErrInvalid)
			}
			return v, nil
		case token.STRING:
			v, err := strconv.Unquote(n.Value)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid string", ErrInvalid)
			}
			return v, nil
		}
	case *ast.UnaryExpr:
		v, err := s.eval(n.X, depth+1)
		if err != nil {
			return nil, err
		}
		switch n.Op {
		case token.NOT:
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: ! requires boolean", ErrInvalid)
			}
			return !b, nil
		case token.SUB:
			f, ok := number(v)
			if !ok {
				return nil, fmt.Errorf("%w: - requires number", ErrInvalid)
			}
			return -f, nil
		case token.ADD:
			f, ok := number(v)
			if !ok {
				return nil, fmt.Errorf("%w: + requires number", ErrInvalid)
			}
			return f, nil
		}
	case *ast.BinaryExpr:
		left, err := s.eval(n.X, depth+1)
		if err != nil {
			return nil, err
		}
		if n.Op == token.LAND {
			b, ok := left.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: && requires booleans", ErrInvalid)
			}
			if !b {
				return false, nil
			}
			right, err := s.eval(n.Y, depth+1)
			if err != nil {
				return nil, err
			}
			b, ok = right.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: && requires booleans", ErrInvalid)
			}
			return b, nil
		}
		if n.Op == token.LOR {
			b, ok := left.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: || requires booleans", ErrInvalid)
			}
			if b {
				return true, nil
			}
			right, err := s.eval(n.Y, depth+1)
			if err != nil {
				return nil, err
			}
			b, ok = right.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: || requires booleans", ErrInvalid)
			}
			return b, nil
		}
		right, err := s.eval(n.Y, depth+1)
		if err != nil {
			return nil, err
		}
		return evalBinary(n.Op, left, right)
	}
	return nil, fmt.Errorf("%w: expression uses unsupported syntax", ErrInvalid)
}

func number(v any) (float64, bool) { f, ok := v.(float64); return f, ok }
func evalBinary(op token.Token, left, right any) (any, error) {
	if op == token.EQL || op == token.NEQ {
		equal := fmt.Sprint(left) == fmt.Sprint(right)
		if op == token.NEQ {
			equal = !equal
		}
		return equal, nil
	}
	a, aok := number(left)
	b, bok := number(right)
	if !aok || !bok {
		return nil, fmt.Errorf("%w: operator %s requires numbers", ErrInvalid, op)
	}
	switch op {
	case token.ADD:
		return a + b, nil
	case token.SUB:
		return a - b, nil
	case token.MUL:
		return a * b, nil
	case token.QUO:
		if b == 0 {
			return nil, fmt.Errorf("%w: division by zero", ErrInvalid)
		}
		return a / b, nil
	case token.REM:
		if b == 0 {
			return nil, fmt.Errorf("%w: division by zero", ErrInvalid)
		}
		return float64(int64(a) % int64(b)), nil
	case token.LSS:
		return a < b, nil
	case token.LEQ:
		return a <= b, nil
	case token.GTR:
		return a > b, nil
	case token.GEQ:
		return a >= b, nil
	}
	return nil, fmt.Errorf("%w: expression uses unsupported operator", ErrInvalid)
}
