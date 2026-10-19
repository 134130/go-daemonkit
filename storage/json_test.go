package storage

import (
	"errors"
	"reflect"
	"testing"

	internal "github.com/134130/go-daemonkit/internal/storage"
)

func TestOperationsAndJSON(t *testing.T) {
	var db internal.Database
	if err := db.Open(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := db.Namespace("test")
	type preferences struct{ Theme string }
	want := preferences{"dark"}
	if err := SetJSON(t.Context(), store, "prefs", want); err != nil {
		t.Fatal(err)
	}
	got, err := GetJSON[preferences](t.Context(), store, "prefs")
	if err != nil || got != want {
		t.Fatalf("GetJSON = %v, %v", got, err)
	}
	if err := SetJSON(t.Context(), store, "prefs", make(chan int)); err == nil {
		t.Fatal("unsupported value encoded")
	}
	got, err = GetJSON[preferences](t.Context(), store, "prefs")
	if err != nil || got != want {
		t.Fatalf("encoding failure changed value: %v, %v", got, err)
	}
	if err := store.Set(t.Context(), "invalid", []byte(`{"Theme":"light","x":`)); err != nil {
		t.Fatal(err)
	}
	if got, err := GetJSON[preferences](t.Context(), store, "invalid"); err == nil || got != (preferences{}) {
		t.Fatalf("decode = %v, %v", got, err)
	}
	if _, err := GetJSON[preferences](t.Context(), store, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	keys, err := store.List(t.Context(), "pr")
	if err != nil || !reflect.DeepEqual(keys, []string{"prefs"}) {
		t.Fatalf("List = %v, %v", keys, err)
	}
	for range 2 {
		if err := store.Delete(t.Context(), "prefs"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Get(t.Context(), "prefs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted = %v", err)
	}
	if err := store.Set(t.Context(), "large", make([]byte, (1<<20)+1)); !errors.Is(err, ErrStorageLimit) {
		t.Fatalf("limit = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(t.Context(), "closed", nil); !errors.Is(err, ErrStorageClosed) {
		t.Fatalf("closed = %v", err)
	}
}
