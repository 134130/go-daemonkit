package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound          = errors.New("storage key not found")
	ErrStorageClosed     = errors.New("storage is not running")
	ErrTransactionClosed = errors.New("storage transaction has ended")
	ErrStorageLimit      = errors.New("storage size limit exceeded")
	// ErrCommitUncertain means the caller must not assume the write was rolled back.
	ErrCommitUncertain = errors.New("storage commit outcome is uncertain")
)

// Store is an App-owned namespace. It is safe for concurrent use and has no Close.
// Values are opaque bytes; serialization belongs to the caller.
type Store struct {
	app       *Database
	namespace string
}

type storageRuntime struct {
	readers, writer *sql.DB
	ctx             context.Context
	cancel          context.CancelFunc
}

// Database owns the storage lifetime. Only the daemon runtime manages it.
type Database struct {
	storageMu sync.Mutex
	storage   *storageRuntime
}

// Namespace returns a handle for a namespace validated by the caller.
func (a *Database) Namespace(namespace string) *Store {
	return &Store{a, namespace}
}

func (a *Database) Open(ctx context.Context, directory string) error {
	path := filepath.Join(directory, "storage.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("storage file must be regular: %s", suffix)
		}
		if err := os.Chmod(path+suffix, 0o600); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	if !strings.HasPrefix(uri.Path, "/") {
		uri.Path = "/" + uri.Path
	}
	params := url.Values{"_pragma": {"busy_timeout(5000)", "synchronous(FULL)"}}
	uri.RawQuery = params.Encode()
	writer, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	writer.SetMaxOpenConns(1)
	fail := func(err error) error { return errors.Join(err, writer.Close()) }
	var mode string
	if err := writer.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
		return fail(err)
	}
	if mode != "wal" {
		return fail(fmt.Errorf("SQLite WAL mode is required"))
	}
	var version int
	if err := writer.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != 0 && version != 1 {
		return fail(fmt.Errorf("unsupported storage schema version %d", version))
	}
	if _, err := writer.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS daemon_kv (
 namespace TEXT NOT NULL, key TEXT NOT NULL, value BLOB NOT NULL,
 PRIMARY KEY(namespace,key)
 ) WITHOUT ROWID; PRAGMA user_version = 1;`); err != nil {
		return fail(err)
	}
	params.Add("_pragma", "query_only(1)")
	uri.RawQuery = params.Encode()
	readers, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return fail(err)
	}
	readers.SetMaxOpenConns(4)
	if err := readers.PingContext(ctx); err != nil {
		_ = readers.Close()
		return fail(err)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	a.storageMu.Lock()
	a.storage = &storageRuntime{readers, writer, lifetime, cancel}
	a.storageMu.Unlock()
	return nil
}

func (a *Database) Close() error {
	a.storageMu.Lock()
	runtime := a.storage
	a.storage = nil
	a.storageMu.Unlock()
	if runtime == nil {
		return nil
	}
	runtime.cancel()
	return errors.Join(runtime.readers.Close(), runtime.writer.Close())
}

// ReadTx provides a snapshot within one callback. Do not retain it or use the
// outer Store inside the callback. Keys are UTF-8, nonempty, at most 1 KiB.
// List returns at most 1,000 sorted keys, or ErrStorageLimit (never a partial list).
type ReadTx struct{ state *storageTransaction }

// WriteTx adds writes to the snapshot. Values may be empty and are limited to 1 MiB.
// Transactions must not be nested or shared between goroutines.
type WriteTx struct{ ReadTx }

type storageTransaction struct {
	mu        sync.Mutex
	conn      *sql.Conn
	ctx       context.Context
	namespace string
	writable  bool
	ended     bool
	failure   error
}

// View calls fn once against a consistent snapshot. It never permits writes.
func (s *Store) View(ctx context.Context, fn func(*ReadTx) error) error {
	if fn == nil {
		return fmt.Errorf("nil storage callback")
	}
	return s.transact(ctx, false, func(tx *ReadTx) error { return fn(tx) })
}

// Update reserves the writer before invoking fn once. It commits only when fn
// and every write succeed and ctx remains active. Errors roll back; panics roll
// back then propagate. Commit errors may wrap ErrCommitUncertain; never replay
// external effects blindly. Tx handles expire when the callback returns.
func (s *Store) Update(ctx context.Context, fn func(*WriteTx) error) error {
	if fn == nil {
		return fmt.Errorf("nil storage callback")
	}
	return s.transact(ctx, true, func(tx *ReadTx) error { return fn(&WriteTx{*tx}) })
}

func (s *Store) transact(ctx context.Context, write bool, fn func(*ReadTx) error) (err error) {
	s.app.storageMu.Lock()
	runtime := s.app.storage
	s.app.storageMu.Unlock()
	if runtime == nil {
		return ErrStorageClosed
	}
	ctx, cancel := context.WithCancel(ctx)
	unlink := context.AfterFunc(runtime.ctx, cancel)
	defer unlink()
	defer cancel()
	if runtime.ctx.Err() != nil {
		return ErrStorageClosed
	}
	pool, begin := runtime.readers, "BEGIN"
	if write {
		pool, begin = runtime.writer, "BEGIN IMMEDIATE"
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, begin); err != nil {
		// A canceled BEGIN may have executed; closing the driver connection rolls it back.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	state := &storageTransaction{conn: conn, ctx: ctx, namespace: s.namespace, writable: write}
	committed := false
	// SQL is issued on one leased connection so rollback remains possible after cancellation.
	defer func() {
		state.mu.Lock()
		state.ended = true
		state.mu.Unlock()
		if committed {
			return
		}
		cleanup, end := context.WithTimeout(context.Background(), 5*time.Second)
		defer end()
		if _, rollbackErr := conn.ExecContext(cleanup, "ROLLBACK"); rollbackErr != nil {
			// Never return a connection with uncertain transaction state to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, ErrCommitUncertain, fmt.Errorf("storage rollback: %w", rollbackErr))
		}
	}()
	err = fn(&ReadTx{state})
	state.mu.Lock()
	state.ended = true
	writeErr := state.failure
	state.mu.Unlock()
	if err = errors.Join(err, writeErr, ctx.Err()); err != nil {
		return err
	}
	if write {
		if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
			return errors.Join(ErrCommitUncertain, err)
		}
		committed = true
	}
	return nil
}

func validStorageKey(key string, empty bool) error {
	if (!empty && key == "") || len(key) > 1024 || !utf8.ValidString(key) || strings.ContainsRune(key, 0) {
		return fmt.Errorf("storage key must be valid UTF-8, at most 1 KiB, without NUL")
	}
	return nil
}

func (tx *ReadTx) Get(key string) ([]byte, error) {
	if tx == nil || tx.state == nil {
		return nil, ErrTransactionClosed
	}
	s := tx.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil, ErrTransactionClosed
	}
	if err := validStorageKey(key, false); err != nil {
		return nil, err
	}
	var value []byte
	err := s.conn.QueryRowContext(s.ctx, "SELECT value FROM daemon_kv WHERE namespace=? AND key=?", s.namespace, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return value, err
}

func (tx *ReadTx) List(prefix string) ([]string, error) {
	if tx == nil || tx.state == nil {
		return nil, ErrTransactionClosed
	}
	s := tx.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil, ErrTransactionClosed
	}
	if err := validStorageKey(prefix, true); err != nil {
		return nil, err
	}
	rows, err := s.conn.QueryContext(s.ctx, "SELECT key FROM daemon_kv WHERE namespace=? AND substr(key,1,length(?))=? ORDER BY key LIMIT 1001", s.namespace, prefix, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
		if len(keys) > 1000 {
			return nil, ErrStorageLimit
		}
	}
	return keys, rows.Err()
}

func (tx *WriteTx) Set(key string, value []byte) error {
	if value == nil {
		value = []byte{}
	}
	return tx.write(key, value, false)
}

func (tx *WriteTx) Delete(key string) error { return tx.write(key, nil, true) }

func (tx *WriteTx) write(key string, value []byte, remove bool) (err error) {
	if tx == nil || tx.state == nil {
		return ErrTransactionClosed
	}
	s := tx.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return ErrTransactionClosed
	}
	if !s.writable {
		return fmt.Errorf("read-only storage transaction")
	}
	defer func() { s.failure = errors.Join(s.failure, err) }()
	if err := validStorageKey(key, false); err != nil {
		return err
	}
	if len(value) > 1<<20 {
		return ErrStorageLimit
	}
	if remove {
		_, err = s.conn.ExecContext(s.ctx, "DELETE FROM daemon_kv WHERE namespace=? AND key=?", s.namespace, key)
	} else {
		_, err = s.conn.ExecContext(s.ctx, "INSERT INTO daemon_kv(namespace,key,value) VALUES(?,?,?) ON CONFLICT(namespace,key) DO UPDATE SET value=excluded.value", s.namespace, key, value)
	}
	return err
}
