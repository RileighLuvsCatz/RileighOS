// Command lifeos is the Phase 1 CLI for LifeOS.
//
// It talks only to the Store interface — never to SQL or JSON directly.
// The storage backend is chosen in exactly one place (openStore), so
// swapping backends is a one-line change:
//
//	lifeos --backend json ...   # flat JSON file (the starting point)
//	lifeos --backend sqlite ... # SQLite file (the default)
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const usage = `lifeos — personal ADHD productivity tool (phase 1: local CLI)

usage:
  lifeos [--backend sqlite|json] [--db PATH] [--json PATH] <todo|note> <command> [args]

global flags:
  --backend sqlite|json   storage backend (default "sqlite")
  --db PATH               sqlite file (default "lifeos.db")
  --json PATH             json file for --backend json (default "lifeos.json")

todo commands:
  lifeos todo add <text>            add a todo
  lifeos todo list [--all|--done|--open]
  lifeos todo done <id>             mark a todo done
  lifeos todo undone <id>           mark a todo not done
  lifeos todo delete <id>

note commands:
  lifeos note add <text>            add a note
  lifeos note list
  lifeos note show <id>
  lifeos note delete <id>
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Defaults: SQLite wins per the Phase 1 "Done when" criteria.
	backend := "sqlite"
	dbPath := envOr("LIFEOS_DB_PATH", "lifeos.db")
	jsonPath := envOr("LIFEOS_JSON_PATH", "lifeos.json")

	// Parse global flags (everything before <todo|note>).
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--backend":
			if len(args) < 2 {
				return errors.New("--backend needs a value: sqlite or json")
			}
			backend = args[1]
			args = args[2:]
		case "--db":
			if len(args) < 2 {
				return errors.New("--db needs a file path")
			}
			dbPath = args[1]
			args = args[2:]
		case "--json":
			if len(args) < 2 {
				return errors.New("--json needs a file path")
			}
			jsonPath = args[1]
			args = args[2:]
		case "-h", "--help", "help":
			fmt.Print(usage)
			return nil
		default:
			return fmt.Errorf("unknown flag %q\n\n%s", args[0], usage)
		}
	}

	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	// THE one-line swap: pick a backend, then everything below
	// uses it through the Store interface only.
	store, err := openStore(backend, dbPath, jsonPath)
	if err != nil {
		return err
	}
	defer store.Close()

	resource, args := strings.ToLower(args[0]), args[1:]
	switch resource {
	case "todo", "todos":
		return runTodo(store, args)
	case "note", "notes":
		return runNote(store, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown resource %q (want todo or note)\n\n%s", resource, usage)
	}
}

// openStore is the single place where the storage backend is chosen.
// To change the app's storage, change this function — nothing else
// in main.go knows which backend is in use.
func openStore(backend, dbPath, jsonPath string) (Store, error) {
	switch strings.ToLower(backend) {
	case "sqlite":
		return OpenSQLiteStore(dbPath)
	case "json":
		return OpenJSONStore(jsonPath)
	default:
		return nil, fmt.Errorf("unknown backend %q (want sqlite or json)", backend)
	}
}

func runTodo(s Store, args []string) error {
	if len(args) == 0 {
		return errors.New("todo needs a command: add, list, done, undone, delete")
	}
	cmd, args := strings.ToLower(args[0]), args[1:]
	switch cmd {
	case "add":
		if len(args) == 0 {
			return errors.New("usage: lifeos todo add <text>")
		}
		t, err := s.AddTodo(strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Printf("added todo %d\n", t.ID)
		return nil
	case "list", "ls":
		filter := "all"
		for _, a := range args {
			switch a {
			case "--all", "--done", "--open":
				filter = strings.TrimPrefix(a, "--")
			default:
				return errors.New("usage: lifeos todo list [--all|--done|--open]")
			}
		}
		todos, err := s.GetTodos()
		if err != nil {
			return err
		}
		shown := 0
		for _, t := range todos {
			switch filter {
			case "done":
				if !t.Done {
					continue
				}
			case "open":
				if t.Done {
					continue
				}
			}
			box := " "
			if t.Done {
				box = "x"
			}
			fmt.Printf("[%s] %d %s\n", box, t.ID, t.Content)
			shown++
		}
		if shown == 0 {
			fmt.Println("(no todos)")
		}
		return nil
	case "done":
		id, err := needID(args, "usage: lifeos todo done <id>")
		if err != nil {
			return err
		}
		if err := s.MarkTodoDone(id); err != nil {
			return friendlyNotFound(err, "todo", id)
		}
		fmt.Printf("todo %d done\n", id)
		return nil
	case "undone":
		id, err := needID(args, "usage: lifeos todo undone <id>")
		if err != nil {
			return err
		}
		if err := s.MarkTodoUndone(id); err != nil {
			return friendlyNotFound(err, "todo", id)
		}
		fmt.Printf("todo %d marked not done\n", id)
		return nil
	case "delete", "del", "rm":
		id, err := needID(args, "usage: lifeos todo delete <id>")
		if err != nil {
			return err
		}
		if err := s.DeleteTodo(id); err != nil {
			return friendlyNotFound(err, "todo", id)
		}
		fmt.Printf("deleted todo %d\n", id)
		return nil
	default:
		return fmt.Errorf("unknown todo command %q (want add, list, done, undone, delete)", cmd)
	}
}

func runNote(s Store, args []string) error {
	if len(args) == 0 {
		return errors.New("note needs a command: add, list, show, delete")
	}
	cmd, args := strings.ToLower(args[0]), args[1:]
	switch cmd {
	case "add":
		if len(args) == 0 {
			return errors.New("usage: lifeos note add <text>")
		}
		n, err := s.AddNote(strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Printf("added note %d\n", n.ID)
		return nil
	case "list", "ls":
		if len(args) != 0 {
			return errors.New("usage: lifeos note list")
		}
		notes, err := s.GetNotes()
		if err != nil {
			return err
		}
		if len(notes) == 0 {
			fmt.Println("(no notes)")
			return nil
		}
		for _, n := range notes {
			fmt.Printf("%d %s\n", n.ID, firstLine(n.Content))
		}
		return nil
	case "show", "get":
		id, err := needID(args, "usage: lifeos note show <id>")
		if err != nil {
			return err
		}
		n, err := s.GetNote(id)
		if err != nil {
			return friendlyNotFound(err, "note", id)
		}
		fmt.Printf("%d %s\n", n.ID, n.Content)
		return nil
	case "delete", "del", "rm":
		id, err := needID(args, "usage: lifeos note delete <id>")
		if err != nil {
			return err
		}
		if err := s.DeleteNote(id); err != nil {
			return friendlyNotFound(err, "note", id)
		}
		fmt.Printf("deleted note %d\n", id)
		return nil
	default:
		return fmt.Errorf("unknown note command %q (want add, list, show, delete)", cmd)
	}
}

func needID(args []string, usageMsg string) (int, error) {
	if len(args) != 1 {
		return 0, errors.New(usageMsg)
	}
	id, err := strconv.Atoi(args[0])
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id %q: must be a positive number", args[0])
	}
	return id, nil
}

func friendlyNotFound(err error, kind string, id int) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%s %d does not exist", kind, id)
	}
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
