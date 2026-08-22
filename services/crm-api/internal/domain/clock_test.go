package domain

import (
	"context"
	"testing"
	"time"
)

// A stamp must not carry precision the store cannot keep. PostgreSQL's
// timestamptz holds microseconds; Go's clock offers nanoseconds.
func TestNowStampCarriesOnlyStorablePrecision(t *testing.T) {
	for range 100 {
		if got := nowStamp(); got.Nanosecond()%1000 != 0 {
			t.Fatalf("nowStamp() = %s has sub-microsecond precision the database will discard",
				got.Format(time.RFC3339Nano))
		}
	}
}

// The regression this fixes: a create returns the record it just wrote,
// including UpdatedAt, and Update compares UpdatedAt exactly to detect a
// concurrent edit. When the returned stamp carried nanoseconds the database
// had discarded, the two disagreed and every create-then-update against
// PostgreSQL failed with a conflict that had not happened.
//
// MemStore is enough to pin the invariant: both stores stamp through
// nowStamp, so a value that survives here is one PostgreSQL can also hold.
func TestCreatedRecordTimestampsSurviveAStoreRoundTrip(t *testing.T) {
	s, ctx, scope := NewMemStore(), context.Background(), Scope{TenantID: "t1"}

	created, err := s.CreateAccount(ctx, scope, &Account{Name: "Original", OwnerID: "o"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.UpdatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("CreateAccount returned UpdatedAt=%s, which PostgreSQL would truncate",
			created.UpdatedAt.Format(time.RFC3339Nano))
	}

	// The update the old behaviour rejected: edit the object the create
	// handed back, unchanged except for the edit itself.
	created.Name = "First edit"
	if _, err := s.UpdateAccount(ctx, scope, created); err != nil {
		t.Fatalf("updating the record a create returned must not conflict: %v", err)
	}
}
