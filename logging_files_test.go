package daemon

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

func TestLogRotationRetentionAndFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	r, err := newRotatingLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	for generation := byte(1); generation <= 5; generation++ {
		chunk := bytes.Repeat([]byte{generation}, maxLogWrite)
		for range logFileSize / maxLogWrite {
			if _, err := r.Write(chunk); err != nil {
				t.Fatal(err)
			}
		}
	}
	for index := range logBackups + 1 {
		name := path
		if index > 0 {
			name += "." + strconv.Itoa(index)
		}
		data, err := os.ReadFile(name)
		if err != nil || len(data) != logFileSize || !bytes.Equal(data, bytes.Repeat([]byte{byte(5 - index)}, logFileSize)) {
			t.Fatalf("archive %d invalid: %v", index, err)
		}
	}
	if _, err := os.Stat(path + ".4"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retention exceeded")
	}
	// An invalid archive must prevent further appends, then permit a safe retry.
	if err := os.Remove(path + ".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".1", 0700); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Write([]byte("lost")); err == nil || n != 0 {
		t.Fatal("rotation failure ignored")
	}
	if info, err := os.Stat(path); err != nil || info.Size() != logFileSize {
		t.Fatal("file grew past limit")
	}
	if err := os.Remove(path + ".1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "next" {
		t.Fatalf("retry: %q %v", data, err)
	}
}

func TestRawLogsPruneWhileIdleAndPreserveAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.stderr")
	inherited, err := openLogFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := inherited.Close(); err != nil {
			t.Error(err)
		}
	}()
	data := bytes.Repeat([]byte("x"), logFileSize+1)
	if _, err := inherited.Write(data); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		var w logWriter
		s := w.start(&testLogSink{}, nil, []string{path})
		synctest.Wait()
		time.Sleep(rawLogInterval)
		synctest.Wait()
		if info, err := os.Stat(path); err != nil || info.Size() != 0 {
			t.Fatalf("idle raw cleanup failed: %v", err)
		}
		tail, err := os.ReadFile(path + ".previous")
		if err != nil || !bytes.Equal(tail, data[len(data)-rawLogTail:]) {
			t.Fatal("tail not preserved")
		}
		if _, err := inherited.Write([]byte("after")); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "after" {
			t.Fatalf("inherited append lost after truncate: %q %v", got, err)
		}
		w.stop(s)
		<-s.done
	})
	// Never truncate when preservation fails.
	if _, err := inherited.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".previous", 0700); err != nil {
		t.Fatal(err)
	}
	if err := pruneRawLog(path); err == nil {
		t.Fatal("preservation failure ignored")
	}
	if info, err := os.Stat(path); err != nil || info.Size() <= logFileSize {
		t.Fatal("truncated without preservation")
	}
}

func TestLogFilesRejectSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if f, err := openLogFile(link, os.O_APPEND|os.O_WRONLY); err == nil {
		_ = f.Close()
		t.Fatal("accepted symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "untouched" {
		t.Fatal("target changed")
	}
}

// Ensure an arbitrary io.Writer failure does not require a filesystem failure.
var _ io.WriteCloser = (*testLogSink)(nil)
