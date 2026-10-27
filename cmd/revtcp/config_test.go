package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"revtcp/internal/pki"
)

func configFixture(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	if err := pki.Generate(filepath.Join(dir, "certs"), []string{"localhost"}, 1); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "client.json")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClientFileRelativePathsAndCLIOverrides(t *testing.T) {
	path := configFixture(t, `{"server":"localhost:7443","target":"localhost:8080","pool":1,"max_conns":2,"log_file":"client.log","dial_timeout":"2s"}`)
	if err := run(context.Background(), []string{"client", "--config", path, "--check-config"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "client.log")); err != nil {
		t.Fatal(err)
	}
	// The explicit CLI limit wins over the file and fails validation.
	if err := run(context.Background(), []string{"client", "--config", path, "--pool", "3", "--check-config"}); err == nil {
		t.Fatal("CLI pool override was ignored")
	}
	// A relative CLI certificate path stays relative to the process directory,
	// while absent/default certificate paths above resolve beside the JSON file.
	if err := run(context.Background(), []string{"client", "--config", path, "--cert", "missing-cli-certificate.crt", "--check-config"}); err == nil {
		t.Fatal("CLI certificate override was ignored")
	}
}

func TestClientFileRejectsMistakes(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":    `{"server":"localhost:7443","target":"localhost:8080","poool":16}`,
		"wrong type":       `{"pool":"16"}`,
		"null field":       `{"pool":null}`,
		"null object":      `null`,
		"extra object":     `{} {}`,
		"invalid duration": `{"idle_timeout":"sometimes"}`,
		"empty host":       `{"server":":7443","target":"localhost:8080"}`,
		"zero port":        `{"server":"localhost:0","target":"localhost:8080"}`,
		"negative pool":    `{"server":"localhost:7443","target":"localhost:8080","pool":-1}`,
		"bad log size":     `{"server":"localhost:7443","target":"localhost:8080","log_file":"client.log","log_max_mb":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := configFixture(t, body)
			if err := run(context.Background(), []string{"client", "--config", path, "--check-config"}); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
