package domain

import "testing"

func TestEvaluateExpressionIsBoundedAndNonTuringComplete(t *testing.T) {
	values := CustomValues{"amount": float64(20), "discount": float64(3), "approved": true}
	got, err := EvaluateExpression("approved && amount - discount >= 17", values)
	if err != nil || got != true {
		t.Fatalf("formula = %v, %v; want true, nil", got, err)
	}
	for _, source := range []string{"f()", "obj.x", "items[0]", "func(){}", "for {}"} {
		if _, err := EvaluateExpression(source, values); err == nil {
			t.Errorf("%q was accepted", source)
		}
	}
	if _, err := EvaluateExpression("amount / 0", values); err == nil {
		t.Error("division by zero was accepted")
	}
}

func FuzzEvaluateExpressionNeverPanics(f *testing.F) {
	for _, source := range []string{"amount + 1", "true || false", "(amount > 1) && approved", "x[0]", ""} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		_, _ = EvaluateExpression(source, CustomValues{"amount": float64(1), "approved": true})
	})
}
