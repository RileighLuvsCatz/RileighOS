// Command rileighos is the Phase 2 CLI + server for RileighOS.
//
// It has two modes sharing one binary for now (same machine, localhost):
//
//	rileighos serve ...   # run the HTTP server around the Store
//	rileighos todo ...    # CLI client: HTTP calls to the server, never
//	rileighos note ...    # touching storage directly
//
// The storage backend is chosen in exactly one place (openStore, used only
// by serve), so swapping backends remains a one-line change:
//
//	rileighos serve --backend json   # flat JSON file (the starting point)
//	rileighos serve --backend sqlite # SQLite file (the default)
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const usage = `rileighos — personal ADHD productivity tool (phase 2: client/server)

usage:
  rileighos serve [--backend sqlite|json] [--db PATH] [--json PATH] [--addr HOST:PORT]
  rileighos [--server URL] <todo|note> <command> [args]

serve flags:
  --backend sqlite|json   storage backend (default "sqlite")
  --db PATH               sqlite file (default "rileighos.db")
  --json PATH             json file for --backend json (default "rileighos.json")
  --addr HOST:PORT        listen address (default "localhost:8080")

global flags:
  --server URL            server to talk to (default "http://localhost:8080",
                          env RILEIGHOS_SERVER_URL)

todo commands:
  rileighos todo add <text>            add a todo
  rileighos todo list [--all|--done|--open]
  rileighos todo done <id>             mark a todo done
  rileighos todo undone <id>           mark a todo not done
  rileighos todo delete <id>

note commands:
  rileighos note add <text>            add a note
  rileighos note list
  rileighos note show <id>
  rileighos note delete <id>

checkoff commands:
  rileighos checkoff add <name>        add a daily habit
  rileighos checkoff list              habits with streaks
  rileighos checkoff show <id>
  rileighos checkoff check <id> [YYYY-MM-DD]
  rileighos checkoff uncheck <id> [YYYY-MM-DD]
  rileighos checkoff delete <id>

  rileighos today                      open todos + check-off streaks
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// The CLI is a pure HTTP client in Phase 2: the only thing it needs
	// to know is where the server lives.
	serverURL := envOr("RILEIGHOS_SERVER_URL", "http://localhost:8080")

	// Parse global flags (everything before <serve|todo|note>).
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--server":
			if len(args) < 2 {
				return errors.New("--server needs a URL, e.g. --server http://localhost:8080")
			}
			serverURL = args[1]
			args = args[2:]
		case "-h", "--help", "help":
			fmt.Print(usage)
			return nil
		case "--backend", "--db", "--json":
			// These moved to `serve` in the client/server split; fail
			// loudly instead of silently ignoring them.
			return fmt.Errorf("%s is a `rileighos serve` flag now — the CLI only needs --server\n\n%s", args[0], usage)
		default:
			return fmt.Errorf("unknown flag %q\n\n%s", args[0], usage)
		}
	}

	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	resource, args := strings.ToLower(args[0]), args[1:]
	switch resource {
	case "serve":
		return runServe(args)
	case "todo", "todos":
		client := NewClient(serverURL)
		defer client.Close()
		return runTodo(client, args)
	case "note", "notes":
		client := NewClient(serverURL)
		defer client.Close()
		return runNote(client, args)
	case "checkoff", "checkoffs":
		client := NewClient(serverURL)
		defer client.Close()
		return runCheckoff(client, args)
	case "today":
		if len(args) != 0 {
			return errors.New("usage: rileighos today")
		}
		client := NewClient(serverURL)
		defer client.Close()
		return runToday(client)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown resource %q (want serve, todo, note, checkoff or today)\n\n%s", resource, usage)
	}
}

// runServe starts the HTTP server around the Store. Storage flags live here
// now — this is the only path that touches a backend directly.
func runServe(args []string) error {
	backend := "sqlite"
	dbPath := envOr("RILEIGHOS_DB_PATH", "rileighos.db")
	jsonPath := envOr("RILEIGHOS_JSON_PATH", "rileighos.json")
	addr := envOr("RILEIGHOS_ADDR", "localhost:8080")

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
		case "--addr":
			if len(args) < 2 {
				return errors.New("--addr needs a value, e.g. --addr localhost:8080")
			}
			addr = args[1]
			args = args[2:]
		case "-h", "--help", "help":
			fmt.Print(usage)
			return nil
		default:
			return fmt.Errorf("unknown serve flag %q\n\n%s", args[0], usage)
		}
	}
	if len(args) != 0 {
		return fmt.Errorf("unexpected argument %q\n\n%s", args[0], usage)
	}

	// THE one-line swap: pick a backend, then everything below
	// uses it through the Store interface only.
	store, err := openStore(backend, dbPath, jsonPath)
	if err != nil {
		return err
	}
	defer store.Close()

	srv := &http.Server{
		Addr:              addr,
		Handler:           NewServer(store).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("rileighos server listening on http://%s (backend %s)\n", addr, backend)
	return srv.ListenAndServe()
}

// openStore is the single place where the storage backend is chosen.
// Only runServe calls it — the todo/note CLI paths talk HTTP and never
// touch a backend directly.
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
			return errors.New("usage: rileighos todo add <text>")
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
				return errors.New("usage: rileighos todo list [--all|--done|--open]")
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
		id, err := needID(args, "usage: rileighos todo done <id>")
		if err != nil {
			return err
		}
		if err := s.MarkTodoDone(id); err != nil {
			return friendlyNotFound(err, "todo", id)
		}
		fmt.Printf("todo %d done\n", id)
		return nil
	case "undone":
		id, err := needID(args, "usage: rileighos todo undone <id>")
		if err != nil {
			return err
		}
		if err := s.MarkTodoUndone(id); err != nil {
			return friendlyNotFound(err, "todo", id)
		}
		fmt.Printf("todo %d marked not done\n", id)
		return nil
	case "delete", "del", "rm":
		id, err := needID(args, "usage: rileighos todo delete <id>")
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
			return errors.New("usage: rileighos note add <text>")
		}
		n, err := s.AddNote(strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Printf("added note %d\n", n.ID)
		return nil
	case "list", "ls":
		if len(args) != 0 {
			return errors.New("usage: rileighos note list")
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
		id, err := needID(args, "usage: rileighos note show <id>")
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
		id, err := needID(args, "usage: rileighos note delete <id>")
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

func runCheckoff(s Store, args []string) error {
	if len(args) == 0 {
		return errors.New("checkoff needs a command: add, list, show, check, uncheck, delete")
	}
	cmd, args := strings.ToLower(args[0]), args[1:]
	switch cmd {
	case "add":
		if len(args) == 0 {
			return errors.New("usage: rileighos checkoff add <name>")
		}
		c, err := s.AddCheckoff(strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Printf("added checkoff %d\n", c.ID)
		return nil
	case "list", "ls":
		if len(args) != 0 {
			return errors.New("usage: rileighos checkoff list")
		}
		checkoffs, err := s.GetCheckoffs()
		if err != nil {
			return err
		}
		if len(checkoffs) == 0 {
			fmt.Println("(no checkoffs)")
			return nil
		}
		for _, c := range checkoffs {
			days, err := s.GetCheckoffDays(c.ID)
			if err != nil {
				return err
			}
			box := " "
			for _, d := range days {
				if d == Today() {
					box = "x"
					break
				}
			}
			fmt.Printf("[%s] %d %s (streak %d)\n", box, c.ID, c.Name, CurrentStreak(days, Today()))
		}
		return nil
	case "show", "get":
		id, err := needID(args, "usage: rileighos checkoff show <id>")
		if err != nil {
			return err
		}
		c, err := s.GetCheckoff(id)
		if err != nil {
			return friendlyNotFound(err, "checkoff", id)
		}
		days, err := s.GetCheckoffDays(id)
		if err != nil {
			return err
		}
		fmt.Printf("%d %s (streak %d)\n", c.ID, c.Name, CurrentStreak(days, Today()))
		if len(days) == 0 {
			fmt.Println("no days checked yet")
		} else {
			fmt.Printf("checked: %s\n", strings.Join(days, ", "))
		}
		return nil
	case "check":
		id, day, err := needCheckDay(args, "usage: rileighos checkoff check <id> [YYYY-MM-DD]")
		if err != nil {
			return err
		}
		if err := s.CheckDay(id, day); err != nil {
			return friendlyNotFound(err, "checkoff", id)
		}
		fmt.Printf("checked %d for %s\n", id, displayDay(day))
		return nil
	case "uncheck":
		id, day, err := needCheckDay(args, "usage: rileighos checkoff uncheck <id> [YYYY-MM-DD]")
		if err != nil {
			return err
		}
		if err := s.UncheckDay(id, day); err != nil {
			return friendlyNotFound(err, "checkoff", id)
		}
		fmt.Printf("unchecked %d for %s\n", id, displayDay(day))
		return nil
	case "delete", "del", "rm":
		id, err := needID(args, "usage: rileighos checkoff delete <id>")
		if err != nil {
			return err
		}
		if err := s.DeleteCheckoff(id); err != nil {
			return friendlyNotFound(err, "checkoff", id)
		}
		fmt.Printf("deleted checkoff %d\n", id)
		return nil
	default:
		return fmt.Errorf("unknown checkoff command %q (want add, list, show, check, uncheck, delete)", cmd)
	}
}

// needCheckDay parses "<id> [day]": the day is optional and defaults to
// today (empty string, resolved by the store). An explicit day must at
// least look like a date here so typos fail fast in the CLI.
func needCheckDay(args []string, usageMsg string) (int, string, error) {
	if len(args) < 1 || len(args) > 2 {
		return 0, "", errors.New(usageMsg)
	}
	id, err := strconv.Atoi(args[0])
	if err != nil || id <= 0 {
		return 0, "", fmt.Errorf("invalid id %q: must be a positive number", args[0])
	}
	day := ""
	if len(args) == 2 {
		if !ValidDay(args[1]) {
			return 0, "", fmt.Errorf("invalid day %q: want YYYY-MM-DD", args[1])
		}
		day = args[1]
	}
	return id, day, nil
}

// displayDay renders the ""-means-today convention for CLI output.
func displayDay(day string) string {
	if day == "" {
		return Today()
	}
	return day
}

func runToday(s Store) error {
	view, err := s.GetToday()
	if err != nil {
		return err
	}
	fmt.Printf("today %s\n", view.Date)
	fmt.Println("open todos:")
	if len(view.OpenTodos) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, t := range view.OpenTodos {
			fmt.Printf("  [ ] %d %s\n", t.ID, t.Content)
		}
	}
	fmt.Println("check-offs:")
	if len(view.Checkoffs) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, c := range view.Checkoffs {
			box := " "
			if c.CheckedToday {
				box = "x"
			}
			fmt.Printf("  [%s] %d %s (streak %d)\n", box, c.ID, c.Name, c.Streak)
		}
	}
	return nil
}
