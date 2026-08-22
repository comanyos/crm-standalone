package metrics

import "testing"

func TestSQLOperationUsesBoundedLabels(t *testing.T) {
	for input, want := range map[string]string{
		"SELECT * FROM accounts WHERE tenant_id = 'secret'":   "select",
		"INSERT INTO contacts VALUES ('person@example.test')": "insert",
		"WITH x AS (SELECT 1) SELECT * FROM x":                "with",
		"VACUUM":                                              "other",
		"":                                                    "other",
	} {
		if got := sqlOperation(input); got != want {
			t.Errorf("sqlOperation(%q) = %q, want %q", input, got, want)
		}
	}
}
