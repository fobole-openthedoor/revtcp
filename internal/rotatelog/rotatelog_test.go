package rotatelog

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRotationRetentionAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.log")
	w, err := Open(path, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"1111", "2222", "3333", "4444", "5555", "6666", "7777"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, want := range map[string]string{"": "7777", ".1": "55556666", ".2": "33334444"} {
		got, err := os.ReadFile(path + suffix)
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v", suffix, got, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatal("too many backups")
	}
	w, err = Open(path, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("8888")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "77778888" {
		t.Fatalf("restart failed to append: %q", got)
	}
	if _, err := w.Write([]byte("9999")); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path + ".1")
	if string(got) != "77778888" {
		t.Fatalf("restart lost size accounting: %q", got)
	}
}

func TestConcurrentRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.log")
	w, err := Open(path, 64, 3)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 32 {
				if _, err := w.Write([]byte("record\n")); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("after close")); err == nil {
		t.Fatal("write after close succeeded")
	}
	files, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("unexpected number of retained files: %d", len(files))
	}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 64 {
			t.Fatalf("unbounded log file: %s", file)
		}
	}
}
