package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// listAccounts returns all phone numbers with a valid session.json under sessionsDir.
func listAccounts(sessionsDir string) ([]string, error) {
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var phones []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(sessionsDir, e.Name(), "session.json")); err == nil {
			phones = append(phones, e.Name())
		}
	}
	sort.Strings(phones)
	return phones, nil
}

func cmdAccounts(args []string) error {
	baseDir, err := resolveBaseDir()
	if err != nil {
		return err
	}
	sessDir := filepath.Join(baseDir, "sessions")

	sub := "list"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}

	switch sub {
	case "list", "":
		phones, err := listAccounts(sessDir)
		if err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}
		if len(phones) == 0 {
			fmt.Fprintln(os.Stderr, "No authorized accounts. Run: tg-cli auth <phone>")
			return nil
		}
		fc := readFileConfig()
		defaultAcc := firstNonEmpty(os.Getenv("ACCOUNT"), fc.DefaultAccount)
		fmt.Printf("%-20s  %s\n", "ACCOUNT", "STATUS")
		fmt.Println(strings.Repeat("-", 40))
		for _, p := range phones {
			marker := ""
			if p == defaultAcc {
				marker = "  [default]"
			}
			fmt.Printf("%-20s  authorized%s\n", p, marker)
		}
		return nil

	case "use":
		if len(args) == 0 {
			return fmt.Errorf("usage: tg-cli accounts use <phone>")
		}
		phone := args[0]
		if _, err := os.Stat(filepath.Join(sessDir, phone, "session.json")); err != nil {
			return fmt.Errorf("no session for %s — run: tg-cli auth %s", phone, phone)
		}
		fc := readFileConfig()
		fc.DefaultAccount = phone
		if err := writeFileConfig(fc); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Default account set to %s\n", phone)
		return nil

	case "remove":
		if len(args) == 0 {
			return fmt.Errorf("usage: tg-cli accounts remove <phone>")
		}
		phone := args[0]
		if err := os.RemoveAll(filepath.Join(sessDir, phone)); err != nil {
			return fmt.Errorf("remove session: %w", err)
		}
		fc := readFileConfig()
		if fc.DefaultAccount == phone {
			fc.DefaultAccount = ""
			if err := writeFileConfig(fc); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not update default account in config: %v\n", err)
			}
		}
		fmt.Fprintf(os.Stderr, "Session for %s removed\n", phone)
		return nil

	default:
		return fmt.Errorf("unknown subcommand %q — use: list, use, remove", sub)
	}
}
