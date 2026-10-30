package config

import (
	"context"
	"errors"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type settings struct {
	Daemon struct {
		Timeout string `toml:"timeout"`
		Enabled bool   `toml:"enabled"`
	} `toml:"daemon"`
}

func testFile(t *testing.T, data string) *File[settings] {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := New(Options[settings]{Path: path, Validate: func(_ context.Context, c settings) error {
		if c.Daemon.Timeout == "bad" {
			return errors.New("invalid timeout")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSetPreservesDocument(t *testing.T) {
	for _, tc := range []struct{ name, before, key, value, after string }{
		{"comment", "# heading\n[daemon]\n timeout  = '30m' # tail\n", "daemon.timeout", `"60m"`, "# heading\n[daemon]\n timeout  = \"60m\" # tail\n"},
		{"CRLF", "[daemon]\r\ntimeout='30m'\r\n", "daemon.enabled", "false", "[daemon]\r\ntimeout='30m'\r\n\"enabled\" = false\r\n"},
		{"new table", "# heading\n", "daemon.timeout", `'30m'`, "# heading\n[\"daemon\"]\n\"timeout\" = '30m'\n"},
		{"root insertion", "[other]\nx=2\n", "count", "0", "\"count\" = 0\n[other]\nx=2\n"},
		{"quoted key", "'a.b'=1 # c\n", `'a.b'`, "2", "'a.b'=2 # c\n"},
		{"multiline", "text = '''a\nb''' # end\n", "text", `'new'`, "text = 'new' # end\n"},
		{"array", "a = [\n 1, # inner\n 2\n] # outside\n", "a", "[3]", "a = [3] # outside\n"},
		{"inline table", "a = { b = 1 } # outside\n", "a", "{b=2}", "a = {b=2} # outside\n"},
		{"dotted", "a.b = 1\n", "a.c", "2", "a.b = 1\n\"a\".\"c\" = 2\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys, value, err := assignment(tc.key, tc.value)
			if err != nil {
				t.Fatal(err)
			}
			got, err := setValue([]byte(tc.before), keys, value)
			if err != nil || string(got) != tc.after {
				t.Fatalf("got %q, %v; want %q", got, err, tc.after)
			}
		})
	}
}

func TestFileValidationAndGet(t *testing.T) {
	original := "[daemon]\ntimeout='30m' # keep\nenabled=true\n"
	f := testFile(t, original)
	for _, pair := range [][2]string{{"daemon.timeout", `'bad'`}, {"daemon.timeout", "false"}, {"daemon.unknown", "1"}, {"daemon.timeout", "'ok'\nx=1"}, {"daemon.timeout=1 #", "2"}} {
		if err := f.Set(t.Context(), pair[0], pair[1]); err == nil {
			t.Fatalf("accepted %v", pair)
		}
		data, _ := os.ReadFile(f.Path())
		if string(data) != original {
			t.Fatal("failed set changed file")
		}
	}
	if err := f.Set(t.Context(), "daemon.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	got, err := f.Get(t.Context(), "daemon.enabled")
	if err != nil || got != false {
		t.Fatalf("get: %v, %v", got, err)
	}
	if _, err := f.Get(t.Context(), "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.Save(ctx, []byte(original)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := f.Save(t.Context(), []byte(`password="SECRET`)); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsafe parse error: %v", err)
	}
}

func TestPathsAndMissingFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	f, err := New(Options[settings]{AppID: "test", Defaults: func() settings { var c settings; c.Daemon.Timeout = "30m"; return c }})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Load(t.Context())
	if err != nil || got.Daemon.Timeout != "30m" {
		t.Fatalf("defaults: %v %v", got, err)
	}
	if filepath.Base(f.Path()) != "config.toml" || !filepath.IsAbs(f.Path()) {
		t.Fatal(f.Path())
	}
	explicit, err := New(Options[settings]{Path: f.Path()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := explicit.Load(t.Context()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := f.Set(t.Context(), "daemon.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := explicit.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUnsafeFilesAndUnsupportedEdits(t *testing.T) {
	f := testFile(t, "[daemon]\ntimeout='30m'\n")
	link := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Symlink(f.Path(), link); err == nil {
		linked, err := New(Options[settings]{Path: link})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := linked.Load(t.Context()); err == nil {
			t.Fatal("read symlink")
		}
		if err := linked.Save(t.Context(), []byte("")); err == nil {
			t.Fatal("replaced symlink")
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	for _, doc := range []string{"[[a]]\nb=1\n", "a={b=1}\n"} {
		if _, err := setValue([]byte(doc), []string{"a", "b"}, []byte("2")); err == nil {
			t.Fatal("unsafe nested update")
		}
	}
	if err := f.Save(t.Context(), make([]byte, maxFileSize+1)); err == nil {
		t.Fatal("oversize accepted")
	}
}

func FuzzSetValue(f *testing.F) {
	for _, input := range []string{"", "a=1 # keep\n", "[a]\nb=2\n", "a=[1,2]\n", "[a.b]\nx=1\n", "a={b=1}\n"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 65536 {
			t.Skip()
		}
		result, err := setValue([]byte(input), []string{"a"}, []byte("42"))
		if err != nil {
			return
		}
		var decoded map[string]any
		if err := toml.Unmarshal(result, &decoded); err != nil {
			t.Fatalf("invalid output: %v", err)
		}
		if decoded["a"] != int64(42) {
			t.Fatalf("wrong value: %v", decoded["a"])
		}
	})
}
