// Command vault-secrets sets or shows Scribe's application secrets in Vault
// under scribe/<workspace>/. `make vault-secrets` supplies the Vault address
// and tokens:
//
//	VAULT_ADDR=... VAULT_TOKEN=... VAULT_ADMIN_TOKEN=... go run ./cmd/vault-secrets -workspace prod update
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/vaultkv"
	"golang.org/x/term"
)

type field struct {
	key    string
	label  string
	hidden bool
}

type secret struct {
	path   string
	fields []field
}

// secrets are the values the application reads from Vault (see internal/config/vault.go).
var secrets = []secret{
	{"google_oauth", []field{{"client_id", "Google OAuth client ID", false}, {"client_secret", "Google OAuth client secret", true}}},
	{"openai", []field{{"api_key", "OpenAI API key", true}}},
	{"gemini", []field{{"api_key", "Gemini API key", true}}},
	{"database/app", []field{{"password", "Application database password", true}}},
}

type store interface {
	Read(context.Context, string) (map[string]string, error)
	Write(context.Context, string, map[string]string) error
}

// prompter asks for one value; hidden values are not echoed.
type prompter func(label string, hidden bool) (string, error)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "vault-secrets: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("vault-secrets", flag.ContinueOnError)
	workspace := flags.String("workspace", "dev", "Vault-owning workspace: dev or prod")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *workspace != "dev" && *workspace != "prod" {
		return errors.New("-workspace must be dev or prod")
	}
	addr, token := os.Getenv("VAULT_ADDR"), os.Getenv("VAULT_TOKEN")
	if addr == "" || token == "" {
		return errors.New("VAULT_ADDR and VAULT_TOKEN are required; use make vault-secrets")
	}
	client := vaultkv.New(addr, token, "secret", "")
	if admin := os.Getenv("VAULT_ADMIN_TOKEN"); admin != "" {
		client.UseAdminToken(admin)
	}
	prefix := "scribe/" + *workspace

	switch flags.Arg(0) {
	case "", "update":
		return update(ctx, client, prefix, terminalPrompter(os.Stdin, os.Stderr), out)
	case "show":
		return show(ctx, client, prefix, out)
	default:
		return errors.New("usage: vault-secrets [-workspace dev|prod] [update|show]")
	}
}

func update(ctx context.Context, s store, prefix string, prompt prompter, out io.Writer) error {
	for _, sec := range secrets {
		path := prefix + "/" + sec.path
		data, err := read(ctx, s, path)
		if err != nil {
			return err
		}
		changed := false
		for _, f := range sec.fields {
			value, err := prompt(f.label+" (blank keeps current)", f.hidden)
			if err != nil {
				return err
			}
			if value = strings.TrimSpace(value); value != "" {
				data[f.key] = value
				changed = true
			}
		}
		if !changed {
			fmt.Fprintf(out, "Kept %s\n", path)
			continue
		}
		if err := s.Write(ctx, path, data); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(out, "Updated %s\n", path)
	}
	return nil
}

func show(ctx context.Context, s store, prefix string, out io.Writer) error {
	for _, sec := range secrets {
		path := prefix + "/" + sec.path
		data, err := read(ctx, s, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "== %s ==\n", path)
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(out, "%s=%s\n", key, data[key])
		}
	}
	return nil
}

func read(ctx context.Context, s store, path string) (map[string]string, error) {
	data, err := s.Read(ctx, path)
	if vaultkv.IsNotFound(err) || (err == nil && data == nil) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

func terminalPrompter(in *os.File, prompts io.Writer) prompter {
	lines := bufio.NewReader(in)
	return func(label string, hidden bool) (string, error) {
		fmt.Fprintf(prompts, "%s: ", label)
		if hidden && term.IsTerminal(int(in.Fd())) {
			value, err := term.ReadPassword(int(in.Fd()))
			fmt.Fprintln(prompts)
			return string(value), err
		}
		value, err := lines.ReadString('\n')
		if errors.Is(err, io.EOF) {
			err = nil
		}
		return value, err
	}
}
