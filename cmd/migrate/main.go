package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nakedgoat/Project-NeroSplice/internal/config"
	"github.com/nakedgoat/Project-NeroSplice/internal/migrator"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		printUsage()
		return errors.New("missing command")
	}

	command := os.Args[1]
	switch command {
	case "help", "-h", "--help":
		printUsage()
		return nil
	case "init":
		return runInit(os.Args[2:])
	case "preflight", "users", "rooms", "media", "migrate", "status", "passwords":
		return runAction(command, os.Args[2:])
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", command)
	}
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	configPath := fs.String("config", "migration.yaml", "path to write example config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := config.WriteExample(*configPath); err != nil {
		return err
	}
	fmt.Println("wrote", *configPath)
	return nil
}

func runAction(command string, args []string) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := fs.String("config", "migration.yaml", "path to config file")
	dryRun := fs.Bool("dry-run", false, "validate and record state without writing to target")
	timeout := fs.Duration("timeout", 2*time.Minute, "command timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	m, err := migrator.New(cfg, *dryRun)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	switch command {
	case "preflight":
		if err := m.Preflight(ctx); err != nil {
			return err
		}
		fmt.Println("preflight OK")
	case "users":
		if err := m.MigrateUsers(ctx); err != nil {
			return err
		}
		if err := m.WritePasswordReport(); err != nil {
			return err
		}
		fmt.Println("users migrated")
	case "rooms":
		if err := m.MigrateRooms(ctx); err != nil {
			return err
		}
		fmt.Println("rooms migrated")
	case "media":
		if err := m.MigrateMedia(ctx); err != nil {
			return err
		}
		fmt.Println("media migrated")
	case "migrate":
		if err := m.MigrateAll(ctx); err != nil {
			return err
		}
		fmt.Println("migration completed")
	case "status":
		data, err := json.MarshalIndent(m.Status(), "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	case "passwords":
		if err := m.WritePasswordReport(); err != nil {
			return err
		}
		fmt.Println("password report written")
	}

	return nil
}

func printUsage() {
	fmt.Print(`matrix-migrator

Usage:
  matrix-migrator <command> [flags]

Commands:
  init       write an example migration config
  preflight  validate source and target connectivity
  users      migrate local users
  rooms      migrate rooms and replay selected state
  media      migrate user avatar media
  migrate    run users, rooms, and media
  status     print migration_state.json contents
  passwords  write temp password CSV from migration state
`)
}
