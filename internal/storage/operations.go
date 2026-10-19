package storage

import "context"

// Get reads one value in a snapshot.
func (s *Store) Get(ctx context.Context, key string) (value []byte, err error) {
	err = s.View(ctx, func(tx *ReadTx) error {
		value, err = tx.Get(key)
		return err
	})
	if err != nil {
		return nil, err
	}
	return value, nil
}

// Set atomically replaces one value.
func (s *Store) Set(ctx context.Context, key string, value []byte) error {
	return s.Update(ctx, func(tx *WriteTx) error { return tx.Set(key, value) })
}

// Delete removes a key. Missing keys succeed.
func (s *Store) Delete(ctx context.Context, key string) error {
	return s.Update(ctx, func(tx *WriteTx) error { return tx.Delete(key) })
}

// List returns sorted keys matching a literal prefix, subject to the transaction limit.
func (s *Store) List(ctx context.Context, prefix string) (keys []string, err error) {
	err = s.View(ctx, func(tx *ReadTx) error {
		keys, err = tx.List(prefix)
		return err
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}
