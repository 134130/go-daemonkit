package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Get reads a dotted TOML key from the file, without defaults or runtime state.
// Quoted key components are supported. Missing keys return os.ErrNotExist.
func (f *File[T]) Get(ctx context.Context, key string) (any, error) {
	keys, _, err := assignment(key, "0")
	if err != nil {
		return nil, err
	}
	data, err := f.read(ctx)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return nil, errors.New("invalid TOML")
	}
	var value any = root
	for _, part := range keys {
		table, ok := value.(map[string]any)
		if !ok {
			return nil, os.ErrNotExist
		}
		value, ok = table[part]
		if !ok {
			return nil, os.ErrNotExist
		}
	}
	return value, ctx.Err()
}

// Set replaces a value using TOML literal syntax and validates the entire result.
// Bytes outside the replaced value are preserved, including trailing comments.
// Missing keys/tables are inserted. Array-table elements and edits inside inline
// tables are unsupported; replace an entire inline table or array instead.
func (f *File[T]) Set(ctx context.Context, key, literal string) error {
	keys, value, err := assignment(key, literal)
	if err != nil {
		return err
	}
	data, err := f.read(ctx)
	if err != nil {
		return err
	}
	next, err := setValue(data, keys, value)
	if err != nil {
		return err
	}
	return f.Save(ctx, next)
}

func assignment(key, literal string) ([]string, []byte, error) {
	if len(key)+len(literal) > maxFileSize {
		return nil, nil, errors.New("config assignment exceeds 1 MiB")
	}
	data := []byte(key + " = " + literal)
	var valid map[string]any
	if err := toml.Unmarshal(data, &valid); err != nil {
		return nil, nil, errors.New("invalid TOML key or value")
	}
	var parser unstable.Parser
	parser.Reset(data)
	if !parser.NextExpression() || parser.Expression().Kind != unstable.KeyValue {
		return nil, nil, errors.New("expected one TOML assignment")
	}
	node := parser.Expression()
	keys := nodeKeys(node)
	start, end := valueRange(data, node)
	value := bytes.Clone(data[start:end])
	if parser.NextExpression() || parser.Error() != nil {
		return nil, nil, errors.New("expected one TOML assignment")
	}
	// Prevent key text from injecting an assignment or value text from adding keys.
	var keyParser unstable.Parser
	keyParser.Reset([]byte("[" + key + "]"))
	if !keyParser.NextExpression() || keyParser.Expression().Kind != unstable.Table || !slices.Equal(keys, nodeKeys(keyParser.Expression())) || keyParser.NextExpression() || keyParser.Error() != nil {
		return nil, nil, errors.New("invalid TOML key")
	}

	return keys, value, nil
}

func nodeKeys(node *unstable.Node) []string {
	var keys []string
	it := node.Key()
	for it.Next() {
		keys = append(keys, string(it.Node().Data))
	}
	return keys
}

// The parser owns expression boundaries, including multiline/composite values.
func valueRange(data []byte, node *unstable.Node) (int, int) {
	it := node.Key()
	start := 0
	for it.Next() {
		raw := it.Node().Raw
		start = int(raw.Offset + raw.Length)
	}
	for data[start] == ' ' || data[start] == '\t' {
		start++
	}
	start++ // '=' follows the last parsed key.
	for data[start] == ' ' || data[start] == '\t' {
		start++
	}
	return start, int(node.Raw.Offset + node.Raw.Length)
}

func keyText(keys []string) string {
	parts := make([]string, len(keys))
	for i, key := range keys {
		data, _ := json.Marshal(key)
		parts[i] = string(data)
	}
	return strings.Join(parts, ".")
}

func setValue(data []byte, keys []string, value []byte) ([]byte, error) {
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return nil, errors.New("invalid TOML")
	}
	var parser unstable.Parser
	parser.Reset(data)
	var table []string
	array := false
	insertion, prefix := len(data), 0
	firstTable := len(data)
	best := -1
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			offset := int(node.Raw.Offset)
			firstTable = min(firstTable, offset)
			if best >= 0 && insertion == len(data) {
				insertion = offset
			}
			table = nodeKeys(node)
			array = node.Kind == unstable.ArrayTable
			if array && len(keys) >= len(table) && slices.Equal(keys[:len(table)], table) {
				return nil, errors.New("setting array-table elements is unsupported")
			}
			if !array && len(table) < len(keys) && slices.Equal(table, keys[:len(table)]) && len(table) > best {
				best, prefix, insertion = len(table), len(table), len(data)
			}
			continue
		}
		if node.Kind != unstable.KeyValue {
			continue
		}
		full := append(slices.Clone(table), nodeKeys(node)...)
		if slices.Equal(full, keys) {
			if array {
				return nil, errors.New("setting array-table elements is unsupported")
			}
			start, end := valueRange(data, node)
			return bytes.Join([][]byte{data[:start], value, data[end:]}, nil), nil
		}
	}
	if parser.Error() != nil {
		return nil, errors.New("invalid TOML")
	}
	newline := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		newline = "\r\n"
	}
	header := ""
	if best < 0 {
		insertion = firstTable
	}
	var parent any = root
	missingParent := false
	for _, part := range keys[:len(keys)-1] {
		object, ok := parent.(map[string]any)
		if !ok {
			return nil, errors.New("key parent is not a table")
		}
		parent, ok = object[part]
		if !ok {
			missingParent = true
			break
		}
	}
	if missingParent {
		insertion, prefix = len(data), len(keys)-1
		header = "[" + keyText(keys[:prefix]) + "]" + newline
	}
	addition := header + keyText(keys[prefix:]) + " = " + string(value) + newline
	if insertion > 0 && data[insertion-1] != '\n' {
		addition = newline + addition
	}
	next := bytes.Join([][]byte{data[:insertion], []byte(addition), data[insertion:]}, nil)
	if err := toml.Unmarshal(next, &root); err != nil {
		return nil, errors.New("key cannot be inserted without restructuring TOML; use edit")
	}
	return next, nil
}
