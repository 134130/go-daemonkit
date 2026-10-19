package storage

import (
	"errors"
	"testing"
)

func TestStorageCommitFailureDoesNotReplay(t *testing.T) {
	var db Database
	if err := db.Open(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := db.Namespace("test")
	calls := 0
	err := store.Update(t.Context(), func(tx *WriteTx) error {
		calls++
		// End the underlying transaction to inject a COMMIT failure at the SQL boundary.
		_, err := tx.state.conn.ExecContext(t.Context(), "ROLLBACK")
		return err
	})
	if !errors.Is(err, ErrCommitUncertain) || calls != 1 {
		t.Fatalf("commit error = %v, attempts = %d", err, calls)
	}
	if err := store.Update(t.Context(), func(tx *WriteTx) error { return tx.Set("healthy", nil) }); err != nil {
		t.Fatalf("connection pool did not recover: %v", err)
	}
}
