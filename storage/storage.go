// Package storage provides namespaced transactional storage owned by daemon.App.
package storage

import internal "github.com/134130/go-daemonkit/internal/storage"

// Store is a concurrent namespace handle. Its lifetime belongs to the App.
type Store = internal.Store

// ReadTx provides a snapshot for one callback.
type ReadTx = internal.ReadTx

// WriteTx provides atomic writes for one callback.
type WriteTx = internal.WriteTx

var (
	ErrNotFound          = internal.ErrNotFound
	ErrStorageClosed     = internal.ErrStorageClosed
	ErrTransactionClosed = internal.ErrTransactionClosed
	ErrStorageLimit      = internal.ErrStorageLimit
	// ErrCommitUncertain means the caller must not assume the write was rolled back.
	ErrCommitUncertain = internal.ErrCommitUncertain
)
