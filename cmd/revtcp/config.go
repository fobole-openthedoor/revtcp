package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Apply file values after flag parsing, preserving explicitly supplied CLI
// options. Certificate/log paths from the file are relative to the file itself.
func applyClientFile(fs *flag.FlagSet, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var values map[string]json.RawMessage
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&values); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	if values == nil {
		return errors.New("config must be a JSON object")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("config must contain exactly one JSON object")
	}
	allowed := map[string]bool{}
	for _, name := range strings.Fields("server target server-name cert key ca pool max-conns dial-timeout idle-timeout retry-min retry-max debug log-file log-max-mb log-backups") {
		allowed[name] = true
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for key, raw := range values {
		name := strings.ReplaceAll(key, "_", "-")
		if !allowed[name] || strings.Contains(key, "-") {
			return fmt.Errorf("unknown config field %q (use snake_case)", key)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("config field %q cannot be null", key)
		}
		var value string
		switch fs.Lookup(name).Value.(flag.Getter).Get().(type) {
		case int:
			var n int
			err = json.Unmarshal(raw, &n)
			value = strconv.Itoa(n)
		case bool:
			var b bool
			err = json.Unmarshal(raw, &b)
			value = strconv.FormatBool(b)
		case string, time.Duration:
			err = json.Unmarshal(raw, &value)
		default:
			return fmt.Errorf("unsupported config field %q", key)
		}
		if err != nil {
			return fmt.Errorf("config field %q: %w", key, err)
		}
		if !explicit[name] {
			if err := fs.Set(name, value); err != nil {
				return fmt.Errorf("config field %q: %w", key, err)
			}
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, name := range []string{"cert", "key", "ca", "log-file"} {
		value := fs.Lookup(name).Value.String()
		if !explicit[name] && value != "" && !filepath.IsAbs(value) {
			if err := fs.Set(name, filepath.Join(filepath.Dir(abs), value)); err != nil {
				return err
			}
		}
	}
	return nil
}
