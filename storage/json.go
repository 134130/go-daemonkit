package storage

import (
	"context"
	"encoding/json"
)

// GetJSON decodes one stored value using encoding/json.
// Missing keys return ErrNotFound. Decode errors return the zero value of T.
func GetJSON[T any](ctx context.Context, store *Store, key string) (T, error) {
	var value T
	data, err := store.Get(ctx, key)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

// SetJSON encodes before writing; encoding failures leave the stored value intact.
func SetJSON[T any](ctx context.Context, store *Store, key string, value T) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return store.Set(ctx, key, data)
}
