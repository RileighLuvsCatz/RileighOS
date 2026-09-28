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
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"rdb/rileighos/internal/canvas"
	"rdb/rileighos/internal/client"
	"rdb/rileighos/internal/models"
	"rdb/rileighos/internal/server"
	"rdb/rileighos/internal/store"
)

const usage = `rileighos — personal ADHD productivity tool (phase 2: client/server)

usage:
  rileighos serve [--backend sqlite|json] [--db PATH] [--json PATH] [--addr HOST:PORT]
  rileighos [--server URL] <todo|note> <command> [args]

serve flags:
  --backend sqlite|json   storage backend (default "sqlite")
  --db PATH               sqlite file (default "rileighos.db")
  --json PATH             json file for --backend json (default "rileighos.json")
  --addr HOST:PORT        listen address (default ":8080", all interfaces;
                          use "localhost:8080" to listen locally only)

global flags:
  --server URL            server to talk to (default "http://localhost:8080",
                          env RILEIGHOS_SERVER_URL)
  --no-sync               skip the on-open Canvas auto-sync for this command

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

  rileighos today                      check-off status + todos due today

canvas commands (needs RILEIGHOS_CANVAS_TOKEN on the server):
  rileighos canvas sync                import published assignments
                                       (asks about new courses; empty answer
                                       asks again next sync)
  rileighos canvas courses             list tracked courses and gating state
  rileighos canvas exclude <code|id>   never import a course
  rileighos canvas include <code|id>   import a course (clears pending)
`

// version is stamped at release time via:
// go build -ldflags "-X main.version=vX.Y.Z" ./cmd/rileighos
// Local builds report "dev".
var version = "dev"

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
	noSync := false

	// Parse global flags (everything before <serve|todo|note>).
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--server":
			if len(args) < 2 {
				return errors.New("--server needs a URL, e.g. --server http://localhost:8080")
			}
			serverURL = args[1]
			args = args[2:]
		case "--no-sync":
			noSync = true
			args = args[1:]
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
	if resource != "serve" && !noSync {
		// Every frontend open syncs Canvas when stale: the server owns
		// the schedule via last_sync_at, so concurrent frontends (CLI,
		// TUI, ticker) share one clock. Best-effort — a failed sync
		// warns and the requested command still runs.
		maybeAutoSync(serverURL)
	}
	switch resource {
	case "serve":
		return runServe(args)
	case "todo", "todos":
		client := client.NewClient(serverURL)
		defer client.Close()
		return runTodo(client, args)
	case "note", "notes":
		client := client.NewClient(serverURL)
		defer client.Close()
		return runNote(client, args)
	case "checkoff", "checkoffs":
		client := client.NewClient(serverURL)
		defer client.Close()
		return runCheckoff(client, args)
	case "today":
		if len(args) != 0 {
			return errors.New("usage: rileighos today")
		}
		client := client.NewClient(serverURL)
		defer client.Close()
		return runToday(client)
	case "canvas":
		client := client.NewClient(serverURL)
		defer client.Close()
		return runCanvas(client, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "version", "--version", "-v":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown resource %q (want serve, todo, note, checkoff, canvas, today or version)\n\n%s", resource, usage)
	}
}

// runServe starts the HTTP server around the Store. Storage flags live here
// now — this is the only path that touches a backend directly.
func runServe(args []string) error {
	backend := "sqlite"
	dbPath := envOr("RILEIGHOS_DB_PATH", "rileighos.db")
	jsonPath := envOr("RILEIGHOS_JSON_PATH", "rileighos.json")
	// Default to all interfaces so the server is reachable over Tailscale
	// when it runs on the Pi. Pass --addr localhost:8080 (or set
	// RILEIGHOS_ADDR) to listen locally only during development.
	addr := envOr("RILEIGHOS_ADDR", ":8080")

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
		Handler:           server.NewServer(store).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	// A bare ":8080" means all interfaces; render it as localhost so the
	// startup line stays a clickable URL during local development.
	displayAddr := addr
	if strings.HasPrefix(displayAddr, ":") {
		displayAddr = "localhost" + displayAddr
	}
	fmt.Printf("rileighos server listening on http://%s (backend %s)\n", displayAddr, backend)
	startCanvasTicker(store)
	return srv.ListenAndServe()
}

// startCanvasTicker runs periodic Canvas imports in the background when a
// token is configured: one immediate sync (non-blocking, so startup never
// waits on the network) plus a ticker. Interval comes from
// RILEIGHOS_CANVAS_SYNC_INTERVAL (default 30m, "0" disables); without a
// token it stays silent — Canvas is opt-in.
func startCanvasTicker(s store.FullStore) {
	cv, err := canvas.NewCanvasClientFromEnv()
	if err != nil {
		return
	}
	interval, enabled, err := canvas.ParseInterval(os.Getenv("RILEIGHOS_CANVAS_SYNC_INTERVAL"), 30*time.Minute)
	if err != nil {
		fmt.Printf("canvas auto-sync disabled: bad RILEIGHOS_CANVAS_SYNC_INTERVAL (%v)\n", err)
		return
	}
	if !enabled {
		return
	}
	fmt.Printf("canvas auto-sync every %s\n", canvas.FormatInterval(interval))
	go func() {
		syncOnce := func() {
			res, err := canvas.RunCanvasSync(s, cv, models.SyncModeAuto, nil)
			if err != nil {
				fmt.Printf("canvas auto-sync: %v\n", err)
				return
			}
			if res.Imported+res.Updated+res.Completed+res.Reopened > 0 {
				fmt.Printf("canvas auto-sync: imported %d, updated %d, completed %d, reopened %d\n",
					res.Imported, res.Updated, res.Completed, res.Reopened)
			}
		}
		syncOnce()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			syncOnce()
		}
	}()
}

// openStore is the single place where the storage backend is chosen.
// Only runServe calls it — the todo/note CLI paths talk HTTP and never
// touch a backend directly.
func openStore(backend, dbPath, jsonPath string) (store.FullStore, error) {
	switch strings.ToLower(backend) {
	case "sqlite":
		return store.OpenSQLiteStore(dbPath)
	case "json":
		return store.OpenJSONStore(jsonPath)
	default:
		return nil, fmt.Errorf("unknown backend %q (want sqlite or json)", backend)
	}
}

func runTodo(s store.Store, args []string) error {
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

func runNote(s store.Store, args []string) error {
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
	if errors.Is(err, store.ErrNotFound) {
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

// staleAfter is how long an on-open auto-sync waits since the last sync.
// Overridable via RILEIGHOS_CANVAS_STALE_AFTER ("15m"); unparseable falls
// back to the default so a typo never bricks the CLI.
func staleAfter() time.Duration {
	if raw := os.Getenv("RILEIGHOS_CANVAS_STALE_AFTER"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		fmt.Fprintf(os.Stderr, "warning: ignoring bad RILEIGHOS_CANVAS_STALE_AFTER %q (want e.g. \"15m\")\n", raw)
	}
	return 15 * time.Minute
}

// maybeAutoSync fires one auto-mode import when the server's last sync is
// stale. Silent when Canvas is unconfigured or fresh; warns (never fails)
// when the sync itself errors, so offline use keeps working.
func maybeAutoSync(serverURL string) {
	client := client.NewClient(serverURL)
	defer client.Close()
	status, err := client.GetCanvasStatus()
	if err != nil || !status.Configured {
		return
	}
	var last time.Time
	if status.LastSyncAt != nil {
		last = *status.LastSyncAt
	}
	// canvas.SyncStale lives with the sync engine; the threshold lives here.
	if !canvas.SyncStale(last, time.Now(), staleAfter()) {
		return
	}
	res, err := client.SyncCanvas(models.SyncModeAuto, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: canvas auto-sync failed: %v\n", err)
		return
	}
	if res.Imported+res.Updated+res.Completed+res.Reopened > 0 {
		fmt.Fprintf(os.Stderr, "auto-sync: imported %d, updated %d, completed %d, reopened %d\n",
			res.Imported, res.Updated, res.Completed, res.Reopened)
	}
}

func runCheckoff(s store.Store, args []string) error {
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
				if d == store.Today() {
					box = "x"
					break
				}
			}
			fmt.Printf("[%s] %d %s (streak %d)\n", box, c.ID, c.Name, store.CurrentStreak(days, store.Today()))
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
		fmt.Printf("%d %s (streak %d)\n", c.ID, c.Name, store.CurrentStreak(days, store.Today()))
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
		if !store.ValidDay(args[1]) {
			return 0, "", fmt.Errorf("invalid day %q: want YYYY-MM-DD", args[1])
		}
		day = args[1]
	}
	return id, day, nil
}

// displayDay renders the ""-means-today convention for CLI output.
func displayDay(day string) string {
	if day == "" {
		return store.Today()
	}
	return day
}

func runToday(s store.Store) error {
	view, err := s.GetToday()
	if err != nil {
		return err
	}
	fmt.Printf("today %s\n", view.Date)
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
	fmt.Println("due today:")
	if len(view.DueTodos) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, t := range view.DueTodos {
			fmt.Printf("  [ ] %d %s\n", t.ID, t.Content)
		}
	}
	return nil
}

func runCanvas(s client.CanvasSyncer, args []string) error {
	if len(args) == 0 {
		return errors.New("canvas needs a command: sync, courses, exclude, include")
	}
	cmd, args := strings.ToLower(args[0]), args[1:]
	switch cmd {
	case "sync":
		if len(args) != 0 {
			return errors.New("usage: rileighos canvas sync")
		}
		return runCanvasSync(s)
	case "courses", "list", "ls":
		if len(args) != 0 {
			return errors.New("usage: rileighos canvas courses")
		}
		return runCanvasCourses(s)
	case "exclude", "ignore":
		if len(args) != 1 {
			return errors.New("usage: rileighos canvas exclude <code|id>")
		}
		return runCanvasResolve(s, args[0], true)
	case "include", "unexclude":
		if len(args) != 1 {
			return errors.New("usage: rileighos canvas include <code|id>")
		}
		return runCanvasResolve(s, args[0], false)
	default:
		return fmt.Errorf("unknown canvas command %q (want sync, courses, exclude, include)", cmd)
	}
}

// runCanvasSync refreshes the course list, prompts over pending courses,
// then imports. Prompt answers: y = include, n = exclude, empty (or EOF
// on a pipe) = decide later; later answers stay pending for the next sync.
func runCanvasSync(s client.CanvasSyncer) error {
	courses, err := s.GetCanvasCourses()
	if err != nil {
		return err
	}
	decisions := models.SyncDecisions{}
	reader := bufio.NewReader(os.Stdin)
	for _, c := range courses {
		if !c.PendingConfirm {
			continue
		}
		fmt.Printf("Import course %s (%s)? [y]es / [n]o / [Enter] later: ", c.Code, c.Name)
		line, err := reader.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if err != nil && len(line) == 0 {
			continue // EOF: non-interactive, leave pending
		}
		switch answer {
		case "y", "yes":
			decisions[c.CourseID] = false
		case "n", "no":
			decisions[c.CourseID] = true
		}
	}
	res, err := s.SyncCanvas(models.SyncModeManual, decisions)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d, updated %d, completed %d, reopened %d, skipped %d\n",
		res.Imported, res.Updated, res.Completed, res.Reopened, res.Skipped)
	if res.DetachedTodos+res.DetachedNotes > 0 {
		fmt.Printf("detached %d todos, %d notes to local (excluded courses keep nothing importing)\n",
			res.DetachedTodos, res.DetachedNotes)
	}
	if len(res.PendingCourses) > 0 {
		fmt.Println("still pending (sync again to decide):")
		for _, c := range res.PendingCourses {
			fmt.Printf("  %d %s (%s)\n", c.CourseID, c.Code, c.Name)
		}
	}
	return nil
}

func runCanvasCourses(s client.CanvasSyncer) error {
	courses, err := s.GetCanvasCourses()
	if err != nil {
		return err
	}
	if len(courses) == 0 {
		fmt.Println("(no courses tracked yet — run `rileighos canvas sync`)")
		return nil
	}
	for _, c := range courses {
		state := "active"
		switch {
		case c.Excluded:
			state = "excluded"
		case c.PendingConfirm:
			state = "pending"
		}
		fmt.Printf("%d %s (%s) [%s]\n", c.CourseID, c.Code, c.Name, state)
	}
	return nil
}

// findCanvasCourse resolves a <code|id> argument against the tracked list:
// numeric input matches the Canvas course ID, anything else matches the
// course code case-insensitively.
func findCanvasCourse(courses []models.CanvasCourse, arg string) (models.CanvasCourse, error) {
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, c := range courses {
			if c.CourseID == id {
				return c, nil
			}
		}
		return models.CanvasCourse{}, fmt.Errorf("no tracked course with id %d (see `rileighos canvas courses`)", id)
	}
	for _, c := range courses {
		if strings.EqualFold(c.Code, arg) {
			return c, nil
		}
	}
	return models.CanvasCourse{}, fmt.Errorf("no tracked course %q (see `rileighos canvas courses`)", arg)
}

func runCanvasResolve(s client.CanvasSyncer, arg string, excluded bool) error {
	courses, err := s.GetCanvasCourses()
	if err != nil {
		return err
	}
	c, err := findCanvasCourse(courses, arg)
	if err != nil {
		return err
	}
	res, err := s.ResolveCanvasCourse(c.CourseID, excluded)
	if err != nil {
		return err
	}
	verb := "included"
	if excluded {
		verb = "excluded"
	}
	fmt.Printf("course %s (%s) %s\n", c.Code, c.Name, verb)
	if res.DetachedTodos+res.DetachedNotes > 0 {
		fmt.Printf("detached %d todos, %d notes to local\n", res.DetachedTodos, res.DetachedNotes)
	}
	return nil
}
