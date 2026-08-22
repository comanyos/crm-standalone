package metrics

import "testing"

func TestSQLOperationUsesBoundedLabels(t *testing.T) {
	for input, want := range map[string]string{
		"SELECT * FROM identities WHERE email = 'person@example.test'": "select",
		"UPDATE sessions SET active = false":                           "update",
		"WITH x AS (SELECT 1) SELECT * FROM x":                         "with",
		"VACUUM":                                                       "other",
		"":                                                             "other",
	} {
		if got := sqlOperation(input); got != want {
			t.Errorf("sqlOperation(%q) = %q, want %q", input, got, want)
		}
	}
}
