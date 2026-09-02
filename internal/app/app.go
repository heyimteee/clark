// Package app wires clark together and hosts the CLI commands.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/heyimteee/clark/internal/alert"
	"github.com/heyimteee/clark/internal/assistant"
	"github.com/heyimteee/clark/internal/calendar"
	"github.com/heyimteee/clark/internal/config"
	"github.com/heyimteee/clark/internal/gateway"
	"github.com/heyimteee/clark/internal/imessage"
	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/notify"
	"github.com/heyimteee/clark/internal/ollama"
	"github.com/heyimteee/clark/internal/scheduler"
	"github.com/heyimteee/clark/internal/store"
	"github.com/heyimteee/clark/internal/tools"
	"github.com/heyimteee/clark/internal/voice"
	"github.com/heyimteee/clark/internal/web"
	"github.com/heyimteee/clark/internal/websearch"
	"github.com/heyimteee/clark/internal/whatsapp"
)

// App is the composition root for a clark process.
type App struct {
	cfg     *config.Config
	st      *store.Store
	ast     *assistant.Service
	sched   *scheduler.Scheduler
	version string
}

// New loads config, opens the store, and builds the assistant.
// New builds the application root. version is the build-stamped identifier
// surfaced in the web console header and CLI output.
func New(version string) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	llm := ollama.New(cfg.OllamaURL, cfg.OllamaModel)
	ast, err := assistant.New(cfg, st, llm)
	if err != nil {
		st.Close()
		return nil, err
	}

	registerCurrentTimeTool(ast.Tools())
	registerProtocolsTools(ast, st)

	// Scheduler: fires are master-privileged turns whose results relay to
	// the Master over every wired channel. Built here so tools share the
	// live instance with the web console later.
	sched := scheduler.New(st, func(ctx context.Context, name, task string) {
		reply, err := ast.Reply(ctx, "master@master", task, true)
		if err != nil {
			logging.Log("SCHED", logging.SevWarn, "FIRE", "Schedule failed", "name", name, "error", err)
			_ = ast.RelayToMaster(ctx, fmt.Sprintf("⚠️ Schedule %q failed: %v", name, err))
			return
		}
		if out := strings.TrimSpace(reply); out != "" {
			_ = ast.RelayToMaster(ctx, fmt.Sprintf("⏰ [%s]\n%s", name, out))
		}
	})
	registerScheduleTools(ast, sched)

	if cfg.TavilyAPIKey != "" {
		registerWebSearchTool(ast.Tools(), websearch.New(cfg.TavilyAPIKey))
		logging.Log("CLARK", logging.SevInfo, "TOOLS", "Web search enabled", "provider", "tavily")
	} else {
		logging.Log("CLARK", logging.SevWarn, "TOOLS", "Web search disabled", "reason", "no TAVILY_API_KEY in .env")
	}

	if cfg.MacActionURL != "" {
		registerCalendarTools(ast, cfg.MacActionURL, cfg.MacActionToken)
		logging.Log("CLARK", logging.SevInfo, "TOOLS", "Calendar enabled", "provider", "mac-bridge")
	}

	return &App{cfg: cfg, st: st, ast: ast, sched: sched, version: version}, nil
}

// registerCurrentTimeTool gives the model a clock: without it, "now" is a
// guess — calendar windows start at the wrong minute and RFC3339 args drift
// to UTC instead of the server's local offset.
func registerCurrentTimeTool(reg *tools.Registry) {
	reg.RegisterFunc(
		"current_time",
		"Get the current local date and time. Triggered by 'what time is it', 'what's the date', or before building any time window (calendar from/to, schedules, reminders). Always use the reported offset when constructing RFC3339 times.",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(ctx context.Context, args map[string]any) (string, error) {
			now := time.Now()
			zone, offset := now.Zone()
			return fmt.Sprintf(
				"Now: %s\nRFC3339: %s\nEpoch: %d\nWeekday: %s\nTimezone: %s (UTC%+.2d:%.2d)",
				now.Format("2006-01-02 15:04:05"),
				now.Format(time.RFC3339),
				now.Unix(),
				now.Weekday(),
				zone,
				offset/3600,
				(offset%3600)/60,
			), nil
		},
	)
}

func registerWebSearchTool(reg *tools.Registry, client *websearch.Client) {
	reg.RegisterFunc(
		"web_search",
		"Search the web for current information and return concise, sourced results. Use whenever the answer needs up-to-date facts — news, weather, prices, sports scores, or anything the Master asks to 'google', 'look up', 'find out', or 'check'.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "description": "The search query"},
				"max_results": map[string]any{"type": "integer", "description": "How many results to fetch (default 5)"},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			query := tools.StringArg(args, "query")
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			maxResults := tools.IntArg(args, "max_results", 5)
			if maxResults < 1 || maxResults > 10 {
				maxResults = 5
			}
			results, err := client.Search(ctx, query, maxResults)
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "No results found.", nil
			}
			return websearch.Format(results), nil
		},
	)
}

func registerCalendarTools(ast *assistant.Service, baseURL, token string) {
	client := calendar.NewMacosClient(baseURL, token)
	ast.Tools().RegisterFunc(
		"add_calendar_event",
		"Add an event to the Master's calendar. Triggered by 'add to calendar ...', 'schedule ...', 'create event ...'. Call current_time first and emit start/end RFC3339 with the offset it reports (e.g. 2026-09-03T12:00:00+07:00); never use 'Z' unless the Master means UTC. Only the Master may use this.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":    map[string]any{"type": "string", "description": "Event title"},
				"start":    map[string]any{"type": "string", "description": "Start time as RFC3339 with offset, e.g. 2026-09-01T10:00:00+07:00"},
				"end":      map[string]any{"type": "string", "description": "End time as RFC3339 with offset; must be after start"},
				"location": map[string]any{"type": "string", "description": "Optional location"},
				"notes":    map[string]any{"type": "string", "description": "Optional notes"},
			},
			"required": []string{"title", "start", "end"},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnlyForTool(ctx, ast); err != nil {
				return "", err
			}
			title := tools.StringArg(args, "title")
			startStr := tools.StringArg(args, "start")
			endStr := tools.StringArg(args, "end")
			if title == "" || startStr == "" || endStr == "" {
				return "", fmt.Errorf("title, start, and end are required")
			}
			start, err := time.Parse(time.RFC3339, startStr)
			if err != nil {
				return "", fmt.Errorf("invalid start time: %w", err)
			}
			end, err := time.Parse(time.RFC3339, endStr)
			if err != nil {
				return "", fmt.Errorf("invalid end time: %w", err)
			}
			if !end.After(start) {
				return "", fmt.Errorf("end (%s) must be after start (%s)", endStr, startStr)
			}
			e := calendar.Event{Title: title, Start: start, End: end, Location: tools.StringArg(args, "location"), Notes: tools.StringArg(args, "notes")}
			id, err := client.Create(ctx, e)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Event '%s' created with ID %s.", title, id), nil
		},
	)
	ast.Tools().RegisterFunc(
		"list_calendar_events",
		"List calendar events overlapping a window. Triggered by 'what's on my calendar', 'show my events', 'list events'. Call current_time first to anchor 'now' and emit from/to RFC3339 with the offset it reports. from defaults to start of today, to defaults to 7 days later. The [id:...] in each row is what delete_calendar_event expects. Only the Master may use this.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from":  map[string]any{"type": "string", "description": "Optional start time as RFC3339 with offset, defaults to start of today"},
				"to":    map[string]any{"type": "string", "description": "Optional end time as RFC3339 with offset, defaults to 7 days from start"},
				"limit": map[string]any{"type": "integer", "description": "Optional max events to show"},
			},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnlyForTool(ctx, ast); err != nil {
				return "", err
			}
			from := time.Now()
			if s := tools.StringArg(args, "from"); s != "" {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					from = t
				}
			} else {
				// Default to the start of today so all-day and earlier-today
				// events are not silently cut off by a rolling "now" window.
				y, m, d := from.Date()
				from = time.Date(y, m, d, 0, 0, 0, 0, from.Location())
			}
			to := from.Add(7 * 24 * time.Hour)
			if s := tools.StringArg(args, "to"); s != "" {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					to = t
				}
			}
			events, err := client.List(ctx, from, to)
			if err != nil {
				return "", err
			}
			if len(events) == 0 {
				return "No events found in that window.", nil
			}
			limit := tools.IntArg(args, "limit", 0)
			if limit > 0 && len(events) > limit {
				events = events[:limit]
			}
			var b strings.Builder
			for _, e := range events {
				b.WriteString(formatCalendarEvent(e))
				b.WriteString("\n")
			}
			return b.String(), nil
		},
	)
	ast.Tools().RegisterFunc(
		"delete_calendar_event",
		"Delete a calendar event by the [id:...] shown by list_calendar_events, or by exact title. Triggered by 'delete event ...', 'cancel my meeting ...', 'remove event ...'. Only the Master may use this.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Event ID or title to delete"},
			},
			"required": []string{"id"},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnlyForTool(ctx, ast); err != nil {
				return "", err
			}
			id := tools.StringArg(args, "id")
			if id == "" {
				return "", fmt.Errorf("id is required")
			}
			if err := client.Delete(ctx, id); err != nil {
				return "", err
			}
			return fmt.Sprintf("Event %s deleted.", id), nil
		},
	)
}

// registerProtocolsTools gives the model its self-evolving skill library:
// load before complex tasks, save novel procedures conservatively, refine on
// failure. Clark-origin saves relay a report to the Master — the mandatory
// "report every new protocol" rule is enforced here in code, not only in the
// prompt.
func registerProtocolsTools(ast *assistant.Service, st *store.Store) {
	list := func(ctx context.Context, _ map[string]any) (string, error) {
		if err := masterOnlyForTool(ctx, ast); err != nil {
			return "", err
		}
		protocols, err := st.ListProtocols()
		if err != nil {
			return "", err
		}
		if len(protocols) == 0 {
			return "No protocols saved yet. Save one with save_protocol after solving a reusable task.", nil
		}
		var b strings.Builder
		for _, p := range protocols {
			fmt.Fprintf(&b, "- %s — %s (v%d · used %d× · %s)\n", p.Slug, p.Title, p.Version, p.UseCount, p.Origin)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	ast.Tools().RegisterFunc(
		"list_protocols",
		"List saved skill protocols (reusable step-by-step procedures). Check this before starting any complex multi-step task. Only the Master may use this.",
		map[string]any{"type": "object", "properties": map[string]any{}},
		list,
	)
	ast.Tools().RegisterFunc(
		"load_protocol",
		"Load the full step-by-step body of a skill protocol by slug. Follow the steps when executing the matching task. Only the Master may use this.",
		map[string]any{
			"type":     "object",
			"required": []string{"slug"},
			"properties": map[string]any{
				"slug": map[string]any{"type": "string", "description": "Protocol slug from list_protocols, e.g. morning-news-digest"},
			},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnlyForTool(ctx, ast); err != nil {
				return "", err
			}
			slug := tools.StringArg(args, "slug")
			if slug == "" {
				return "", fmt.Errorf("slug is required")
			}
			p, err := st.GetProtocol(slug)
			if err != nil {
				return "", err
			}
			if err := st.TouchProtocol(p.ID); err != nil {
				return "", err
			}
			return fmt.Sprintf("Protocol %q (v%d, used %d×):\n\n%s", p.Slug, p.Version, p.UseCount+1, p.Body), nil
		},
	)
	ast.Tools().RegisterFunc(
		"save_protocol",
		"Save or update a skill protocol: a concise numbered step-by-step procedure for a recurring task. Updating an existing slug bumps its version. Pass origin='clark' when you save on your own initiative (you must then report it to the Master in your reply); origin='master' when the Master explicitly asked. Only the Master may use this.",
		map[string]any{
			"type":     "object",
			"required": []string{"title", "body"},
			"properties": map[string]any{
				"title":   map[string]any{"type": "string", "description": "Short protocol title, e.g. Morning News Digest"},
				"body":    map[string]any{"type": "string", "description": "Concise numbered markdown steps (max 8KB)"},
				"purpose": map[string]any{"type": "string", "description": "One-line summary of what this protocol achieves"},
				"origin":  map[string]any{"type": "string", "description": "'clark' if self-initiated, 'master' if explicitly requested", "enum": []string{"master", "clark"}},
			},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnlyForTool(ctx, ast); err != nil {
				return "", err
			}
			title := tools.StringArg(args, "title")
			body := tools.StringArg(args, "body")
			purpose := tools.StringArg(args, "purpose")
			origin := tools.StringArg(args, "origin")
			if title == "" || body == "" {
				return "", fmt.Errorf("title and body are required")
			}
			slug := store.SlugifyProtocolTitle(title)
			if len(slug) < 2 {
				return "", fmt.Errorf("title must contain at least two letters or digits")
			}
			if len(body) > 8<<10 {
				return "", fmt.Errorf("protocol body is %d bytes, max is %d", len(body), 8<<10)
			}
			p, err := st.UpsertProtocol(store.Protocol{Slug: slug, Title: title, Body: body, Origin: origin})
			if err != nil {
				return "", err
			}
			if origin == "clark" {
				report := fmt.Sprintf("📓 New protocol saved: %s (%s) — %s", p.Title, p.Slug, purpose)
				if err := ast.RelayToMaster(ctx, report); err != nil {
					return fmt.Sprintf("Protocol %q saved (version %d), but the report to the Master failed: %v", p.Slug, p.Version, err), nil
				}
				return fmt.Sprintf("Protocol %q saved (version %d) and reported to the Master.", p.Slug, p.Version), nil
			}
			return fmt.Sprintf("Protocol %q saved (version %d).", p.Slug, p.Version), nil
		},
	)
	ast.Tools().RegisterFunc(
		"delete_protocol",
		"Delete a skill protocol by id or slug. Only the Master may use this.",
		map[string]any{
			"type":     "object",
			"required": []string{"id"},
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Protocol id or slug"},
			},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnlyForTool(ctx, ast); err != nil {
				return "", err
			}
			raw := tools.StringArg(args, "id")
			if raw == "" {
				return "", fmt.Errorf("id is required")
			}
			id, convErr := strconv.ParseInt(raw, 10, 64)
			if convErr != nil {
				p, err := st.GetProtocol(raw)
				if err != nil {
					return "", err
				}
				id = p.ID
			}
			if err := st.DeleteProtocol(id); err != nil {
				return "", err
			}
			return fmt.Sprintf("Protocol %s deleted.", raw), nil
		},
	)
}

// registerScheduleTools lets the Master create and manage recurring tasks:
// "gather the news and report at 6 AM every day" becomes a cron schedule
// fired as a master-privileged turn whose result is relayed back.
func registerScheduleTools(ast *assistant.Service, sched *scheduler.Scheduler) {
	ast.Tools().RegisterFunc(
		"list_schedules",
		"List recurring scheduled tasks with their cron spec and next run time. Only the Master may use this.",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(ctx context.Context, _ map[string]any) (string, error) {
			if err := masterOnlyForTool(ctx, ast); err != nil {
				return "", err
			}
			rows, next, err := sched.List()
			if err != nil {
				return "", err
			}
			if len(rows) == 0 {
				return "No schedules yet. Create one with create_schedule.", nil
			}
			var b strings.Builder
			for i, sc := range rows {
				nextStr := "—"
				if !next[i].IsZero() {
					nextStr = next[i].Format("2006-01-02 15:04 (-07:00)")
				}
				state := "enabled"
				if !sc.Enabled {
					state = "disabled"
				}
				task := sc.Task
				if len(task) > 80 {
					task = task[:80] + "…"
				}
				fmt.Fprintf(&b, "- %s | %s | %s | next: %s\n  task: %s\n", sc.Name, sc.Spec, state, nextStr, task)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	)
	ast.Tools().RegisterFunc(
		"create_schedule",
		"Create or update a recurring scheduled task. spec is 5-field cron ('0 6 * * *' = every day 06:00 local). Omitted task/spec keep existing values on update — pause by passing enabled=false. Confirm the schedule and its next run to the Master after creating. Only the Master may use this.",
		map[string]any{
			"type":     "object",
			"required": []string{"name", "spec"},
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "Short schedule name, e.g. morning-news"},
				"task":    map[string]any{"type": "string", "description": "The instruction executed at fire time, e.g. 'Run the morning-news protocol: gather current news and report a digest.'"},
				"spec":    map[string]any{"type": "string", "description": "5-field cron spec in local time, e.g. '0 6 * * *'"},
				"enabled": map[string]any{"type": "boolean", "description": "Whether the schedule runs (default true; false pauses it)"},
			},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnlyForTool(ctx, ast); err != nil {
				return "", err
			}
			name := tools.StringArg(args, "name")
			task := tools.StringArg(args, "task")
			spec := tools.StringArg(args, "spec")
			if name == "" || spec == "" {
				return "", fmt.Errorf("name and spec are required")
			}
			var enabled *bool
			if _, ok := args["enabled"]; ok {
				v := tools.BoolArg(args, "enabled")
				enabled = &v
			}
			sc, err := sched.Upsert(name, task, spec, enabled)
			if err != nil {
				return "", err
			}
			state := "enabled"
			if !sc.Enabled {
				state = "disabled (paused)"
			}
			next := "—"
			if n, err := sched.NextRun(sc.Spec); err == nil && sc.Enabled {
				next = n.Format("2006-01-02 15:04 (-07:00)")
			}
			return fmt.Sprintf("Schedule %q saved (%s, %s). Next run: %s.", sc.Name, sc.Spec, state, next), nil
		},
	)
	ast.Tools().RegisterFunc(
		"delete_schedule",
		"Delete a recurring scheduled task by name. Only the Master may use this.",
		map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Schedule name from list_schedules"},
			},
		},
		func(ctx context.Context, args map[string]any) (string, error) {
			if err := masterOnlyForTool(ctx, ast); err != nil {
				return "", err
			}
			name := tools.StringArg(args, "name")
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			if err := sched.Delete(name); err != nil {
				return "", err
			}
			return fmt.Sprintf("Schedule %q deleted.", name), nil
		},
	)
}

func masterOnlyForTool(ctx context.Context, _ *assistant.Service) error {
	if !tools.IsMaster(ctx) {
		return fmt.Errorf("forbidden: only the Master may use this tool")
	}
	return nil
}

// formatCalendarEvent renders one event row for the model: the [id:...] is
// what delete_calendar_event expects, all-day events skip meaningless
// 00:00–23:59 times, and an end on a different day carries its full date
// instead of a bare clock time.
func formatCalendarEvent(e calendar.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s [id:%s]", e.Title, e.ID)
	if e.AllDay {
		fmt.Fprintf(&b, " (all day) | %s", e.Start.Format("2006-01-02"))
	} else {
		fmt.Fprintf(&b, " | %s", e.Start.Format("2006-01-02 15:04"))
		if sameDay := e.End.Year() == e.Start.Year() && e.End.YearDay() == e.Start.YearDay(); sameDay {
			fmt.Fprintf(&b, " → %s", e.End.Format("15:04"))
		} else {
			fmt.Fprintf(&b, " → %s", e.End.Format("2006-01-02 15:04"))
		}
	}
	if e.Location != "" {
		fmt.Fprintf(&b, " @ %s", e.Location)
	}
	return b.String()
}

// Close releases the underlying store.
func (a *App) Close() error {
	return a.st.Close()
}

// Init seeds the assistant's default settings.
func (a *App) Init() error {
	return a.ast.Init()
}

// Run starts the WhatsApp listener and, when enabled, the web console (with
// the iMessage bridge mounted inside it) or the standalone iMessage bridge
// server. All block until ctx is done; a transport error stops the others.
func (a *App) Run() error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}
	// Force status on startup if CLARK_START_STATUS is set. This ensures
	// Clark always starts in a known state after deploys/restarts.
	if err := a.ast.SetStatus(a.cfg.StartStatus); err != nil {
		logging.Log("CLARK", logging.SevWarn, "STATUS", "Failed to apply start status", "error", err)
	}
	if a.ast.Context() == "" {
		return fmt.Errorf("No context yet Sir. Do 'clark ctx -c [context]' first.")
	}

	logging.Log("CLARK", logging.SevInfo, "START", "Assistant started", "name", a.ast.Name())
	logging.Log("CLARK", logging.SevInfo, "CONTEXT", "Master context loaded", "context", a.ast.Context())
	logging.Log("CLARK", logging.SevInfo, "STATUS", "Assistant status", "enabled", a.ast.Enabled())

	// Advertise this process so the `clark` CLI can poke it to reload its cache
	// after changing settings in a separate process. The SIGHUP handler re-reads
	// the store so CLI changes (ctx, toggle, think, ...) take effect live.
	if err := writePidFile(a.cfg); err != nil {
		logging.Log("CLARK", logging.SevWarn, "PID", "Could not write pidfile", "error", err.Error())
	} else {
		defer removePidFile(a.cfg)
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	go func() {
		for range sigCh {
			if err := a.ast.Reload(); err != nil {
				logging.Log("CLARK", logging.SevWarn, "RELOAD", "Failed to reload state from DB", "error", err.Error())
			} else {
				logging.Log("CLARK", logging.SevInfo, "RELOAD", "State reloaded from DB")
			}
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Shared alert service: renders and delivers alerts (bypass command,
	// monitoring webhooks) to WhatsApp, iMessage, the web console chat, and
	// spoken voice (voice mode) or FaceTime + macOS banner (silent mode).
	// The web console wires its chat broadcast; the messengers wire delivery
	// when they come up; the macOS bridge handles FaceTime/banner actions.
	alerts := alert.New(a.ast)
	alerts.SetDesktop(a.notifier().Notify)
	alerts.SetModeReader(func() string { return a.ast.AlertMode() })
	alerts.SetFaceTime(func(number string) error {
		if a.cfg.MacActionURL == "" {
			return nil
		}
		if number == "" {
			number = macPhoneNumber(a.cfg)
		}
		return macAction(a.cfg, map[string]any{"type": "facetime", "number": number})
	})
	alerts.SetBanner(func(title, body string) error {
		if a.cfg.MacActionURL == "" {
			return nil
		}
		return macAction(a.cfg, map[string]any{"type": "banner", "title": title, "body": body})
	})

	// VIP → Master relay and away digests both use the same dual-channel
	// fan-out (WA + iMessage + web) as alerts, but with custom Clark text.
	a.ast.SetRelayFunc(func(ctx context.Context, fromJID, text string) error {
		relation, _ := a.ast.Relation(fromJID)
		prefix := ""
		if relation != "" {
			prefix = relation + ": "
		}
		alerts.Relay(ctx, prefix+text)
		return nil
	})
	a.ast.SetAwaySender(func(ctx context.Context, text string) error {
		alerts.Relay(ctx, text)
		return nil
	})

	// Recurring-task scheduler: start after the relay is wired so fired
	// schedules can reach the Master immediately.
	if a.cfg.SchedulerEnabled {
		go func() {
			if err := a.sched.Start(ctx); err != nil {
				logging.Log("SCHED", logging.SevErr, "START", "Scheduler failed to start", "error", err)
			}
		}()
	} else {
		logging.Log("SCHED", logging.SevInfo, "START", "Scheduler disabled (SCHEDULER_ENABLED)")
	}

	// Calendar proactive ticker: if there are events in the next 24h, ask
	// whether to enter Protocol Away via the LLM tool path.
	if a.cfg.MacActionURL != "" {
		go func() {
			ticker := time.NewTicker(15 * time.Minute)
			defer ticker.Stop()
			lastAsk := time.Time{}
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if time.Since(lastAsk) < 24*time.Hour {
						continue
					}
					// Use the LLM to check calendar via tool: send a synthetic
					// message that will trigger list_calendar_events
					_, err := a.ast.Reply(ctx, "master@master", "Check my calendar for the next 24 hours and if there are events, ask me whether to enter Protocol Away.", true)
					if err != nil {
						logging.Log("CALENDAR", logging.SevWarn, "TICKER", "Failed to check calendar", "error", err)
						continue
					}
					lastAsk = time.Now()
				}
			}
		}()
	}

	errCh := make(chan error, 3)
	go func() {
		errCh <- whatsapp.Run(ctx, whatsapp.Options{
			DBPath:       a.cfg.DBPath,
			Butler:       a.ast,
			Notifier:     alerts,
			Tools:        a.ast.Tools(),
			BypassPhrase: a.cfg.BypassPhrase,
			NameToJID:    a.ast.LookupJID,
			MessengerHook: func(msgr *whatsapp.WAMessenger) {
				alerts.SetWASender(func(ctx context.Context, text string) error {
					return msgr.SendSelf(ctx, text)
				})
			},
		})
	}()

	// One voice engine serves everything: web console STT/TTS and the
	// local media brain (voice-note transcription) via the assistant.
	engine := buildVoiceEngine(a.cfg)
	a.ast.AttachSTT(engine.STT)

	if a.cfg.WebEnabled {
		// Pre-warm any resident TTS daemon (piper, or the failover's piper
		// backup) so the model is loaded before first use.
		if w, ok := engine.TTS.(interface{ Start(context.Context) error }); ok {
			if err := w.Start(ctx); err != nil {
				logging.Log("VOICE", logging.SevWarn, "TTS", "TTS daemon pre-warm failed; will retry on demand", "error", err.Error())
			} else {
				logging.Log("VOICE", logging.SevInfo, "TTS", "TTS daemon pre-warmed")
			}
		}
		// Pre-warm STT daemon so the first transcription is fast (accept ~8s added to boot).
		if s, ok := engine.STT.(interface{ Start(context.Context) error }); ok {
			if err := s.Start(ctx); err != nil {
				logging.Log("VOICE", logging.SevWarn, "STT", "STT daemon pre-warm failed; will retry on demand", "error", err.Error())
			} else {
				logging.Log("VOICE", logging.SevInfo, "STT", "STT daemon pre-warmed")
			}
		}
	}

	// Both transports run concurrently and independently (#57): the console
	// must never gate the bridge API or vice versa.
	err = a.runConsoles(ctx, alerts, engine)
	stop()
	return err
}

// runConsoles serves the web console and the iMessage bridge API in parallel,
// each on its own listener, and returns when the first one stops. Regression
// guard for #57: web.Run is blocking, so it must run in its own goroutine or
// a combined deployment would never reach the bridge.
func (a *App) runConsoles(ctx context.Context, alerts *alert.Service, engine *voice.Engine) error {
	errCh := make(chan error, 2)

	// The dashboard calendar tile reads the Mac bridge directly (same client
	// the LLM tools use) instead of round-tripping through chat.
	var calClient calendar.Client
	if a.cfg.MacActionURL != "" {
		calClient = calendar.NewMacosClient(a.cfg.MacActionURL, a.cfg.MacActionToken)
	}

	if a.cfg.WebEnabled {
		go func() {
			errCh <- web.Run(ctx, web.Options{
				ListenAddr:     a.cfg.WebListenAddr,
				WebToken:       a.cfg.WebToken,
				AlertToken:     a.cfg.AlertToken,
				Butler:         a.ast,
				Store:          a.st,
				Voice:          engine,
				STTModel:       a.cfg.STTModel,
				TTSEngine:      a.cfg.TTSEngine,
				AffirmationDir: a.cfg.AffirmationDir,
				Alerts:         alerts,
				Scheduler:      a.sched,
				Calendar:       calClient,
				Version:        a.version,
			})
		}()
	}

	if a.cfg.IMessageEnabled {
		msgr := imessage.NewMessenger(a.st, a.cfg.IMessageSelfHandle)
		alerts.SetIMessageSender(func(ctx context.Context, text string) error {
			return msgr.SendSelf(ctx, text)
		})
		go func() {
			errCh <- imessage.Run(ctx, imessage.Options{
				Out:          a.st,
				Butler:       a.ast,
				Notifier:     alerts,
				Tools:        a.ast.Tools(),
				SelfHandle:   a.cfg.IMessageSelfHandle,
				BypassPhrase: a.cfg.BypassPhrase,
				ListenAddr:   a.cfg.IMessageListenAddr,
				Token:        a.cfg.IMessageBridgeToken,
				NameToHandle: a.ast.LookupIMessage,
			})
		}()
	}

	return <-errCh
}

// buildVoiceEngine assembles the STT/TTS seam. Engines are only wired when
// their prerequisites exist, so a missing whisper model or piper daemon
// degrades to "voice unavailable" instead of a hard crash.
func buildVoiceEngine(cfg *config.Config) *voice.Engine {
	return &voice.Engine{STT: buildSTTEngine(cfg), TTS: buildTTSEngine(cfg)}
}

func buildSTTEngine(cfg *config.Config) voice.STT {
	switch cfg.STTEngine {
	case "faster-whisper", "":
		if _, err := os.Stat(cfg.WhisperScript); err != nil {
			logging.Log("VOICE", logging.SevWarn, "STT", "Whisper runner missing; STT disabled", "script", cfg.WhisperScript)
			return nil
		}
		if _, err := os.Stat(cfg.WhisperModelDir); err != nil {
			logging.Log("VOICE", logging.SevWarn, "STT", "Whisper model missing; STT disabled", "model", cfg.WhisperModelDir)
			return nil
		}
		logging.Log("VOICE", logging.SevInfo, "STT", "faster-whisper ready", "model", cfg.WhisperModelDir)
		return voice.NewFasterWhisper(cfg.WhisperScript, cfg.WhisperModelDir)
	case "ollama":
		logging.Log("VOICE", logging.SevInfo, "STT", "Ollama whisper wired", "model", cfg.STTModel)
		return voice.NewOllamaWhisper(cfg.OllamaURL, cfg.STTModel)
	default:
		logging.Log("VOICE", logging.SevWarn, "STT", "Unknown STT engine; disabled", "engine", cfg.STTEngine)
		return nil
	}
}

func buildTTSEngine(cfg *config.Config) voice.TTS {
	switch cfg.TTSEngine {
	case "kokoro-remote":
		// Remote Kokoro on the Mac (MLX), with the server-side piper daemon as
		// fallback so TTS still works when the Mac is asleep or unreachable.
		fallback := buildPiper(cfg)
		if cfg.TTSRemoteURL == "" {
			return fallback
		}
		remote := voice.NewKokoroRemote(cfg.TTSRemoteURL, cfg.TTSRemoteToken, cfg.KokoroVoice)
		if fallback == nil {
			return remote
		}
		return voice.NewFailoverTTS(remote, fallback)
	case "piper":
		return buildPiper(cfg)
	default:
		logging.Log("VOICE", logging.SevWarn, "TTS", "Unknown TTS engine; disabled", "engine", cfg.TTSEngine)
		return nil
	}
}

func buildPiper(cfg *config.Config) voice.TTS {
	if _, err := os.Stat(cfg.PiperDaemon); err != nil {
		logging.Log("VOICE", logging.SevWarn, "TTS", "Piper daemon missing; TTS disabled", "script", cfg.PiperDaemon)
		return nil
	}
	if _, err := os.Stat(cfg.PiperVoice); err != nil {
		logging.Log("VOICE", logging.SevWarn, "TTS", "Piper voice missing; TTS disabled", "voice", cfg.PiperVoice)
		return nil
	}
	logging.Log("VOICE", logging.SevInfo, "TTS", "Piper ready", "daemon", cfg.PiperDaemon, "voice", cfg.PiperVoice)
	return voice.NewPiper(cfg.PiperDaemon, cfg.PiperVoice)
}

// notifier picks the desktop notifier, or a silent no-op in headless
// environments (CLARK_NO_NOTIFY=1).
// macPhoneNumber resolves the Master's phone number for a FaceTime call:
// the iMessage self handle (e.g. +628117705636) is the Master's own number.
func macPhoneNumber(cfg *config.Config) string {
	h := cfg.IMessageSelfHandle
	if h == "" {
		return ""
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, h)
	if digits == "" {
		return ""
	}
	return "+" + digits
}

// macAction POSTs an action to the macOS bridge so it can run a FaceTime call
// or show a native banner (things only the Mac's GUI session can do).
func macAction(cfg *config.Config, payload map[string]any) error {
	if cfg.MacActionURL == "" {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.MacActionURL, "/")+"/action", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.MacActionToken != "" {
		req.Header.Set("X-Clark-Bridge-Token", cfg.MacActionToken)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mac bridge action returned %s", resp.Status)
	}
	return nil
}

func (a *App) notifier() gateway.Notifier {
	if a.cfg.NoNotify {
		return notify.Silent{}
	}
	return notify.New()
}

// VIP manages the inner circle.
func (a *App) VIP(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	fs := flag.NewFlagSet("vip", flag.ContinueOnError)
	var addTarget string
	var delTarget string
	var clear bool

	fs.StringVar(&addTarget, "add", "", "Add New VIP")
	fs.StringVar(&addTarget, "a", "", "Add New VIP (shorthand)")
	fs.StringVar(&delTarget, "delete", "", "Delete VIP")
	fs.StringVar(&delTarget, "d", "", "Delete VIP (shorthand)")
	fs.BoolVar(&clear, "clear", false, "Empty the entire VIP list")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing args: %w", err)
	}

	if addTarget == "" && delTarget == "" && !clear {
		fs.Usage()
		return fmt.Errorf("empty input")
	}

	if clear {
		if addTarget != "" || delTarget != "" {
			return fmt.Errorf("cannot mix -clear with -add or -delete")
		}
		if err := a.ast.ClearVIPs(); err != nil {
			return fmt.Errorf("clearing VIP list: %w", err)
		}
		logging.Log("MEMORY", logging.SevInfo, "VIPCLEAR", "Inner circle emptied")
	}

	if addTarget != "" {
		if err := a.ast.AddVIP(addTarget); err != nil {
			return fmt.Errorf("adding new VIP: %w", err)
		}
		logging.Log("MEMORY", logging.SevInfo, "VIPADD", "Added to VIP list", "contact", addTarget)
	}

	if delTarget != "" {
		if err := a.ast.DeleteVIP(delTarget); err != nil {
			return fmt.Errorf("deleting VIP: %w", err)
		}
		logging.Log("MEMORY", logging.SevInfo, "VIPDEL", "Deleted from VIP list", "contact", delTarget)
	}

	logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "Current VIP list")
	for jid, relation := range a.ast.VIPList() {
		logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "VIP entry", "jid", jid, "relation", relation)
	}

	a.notifyRunning()
	return nil
}

// Context updates the master context.
func (a *App) Context(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	fs := flag.NewFlagSet("ctx", flag.ContinueOnError)
	var context string
	var clear bool

	fs.StringVar(&context, "change", "", "Change Context")
	fs.StringVar(&context, "c", "", "Change Context (Shorthand)")
	fs.BoolVar(&clear, "clear", false, "Empty the master context")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if clear {
		if context != "" {
			return fmt.Errorf("cannot mix -clear with -change")
		}
		return a.setAndNotify(a.ast.SetContext(""))
	}

	return a.setAndNotify(a.ast.SetContext(context))
}

// Toggle flips the assistant's enabled status. Bare usage flips everyone;
// -r <name|number> -set on|off sets one VIP's personal status; -all on|off
// sets everyone explicitly and clears personal statuses.
func (a *App) Toggle(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	fs := flag.NewFlagSet("toggle", flag.ContinueOnError)
	var recipient string
	var set string
	var all string

	fs.StringVar(&recipient, "recipient", "", "VIP name or number to toggle individually")
	fs.StringVar(&recipient, "r", "", "VIP name or number (shorthand)")
	fs.StringVar(&set, "set", "", "on or off")
	fs.StringVar(&all, "all", "", "Set everyone explicitly to on or off")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing args: %w", err)
	}

	if recipient != "" && all != "" {
		return fmt.Errorf("cannot mix -r with -all")
	}
	if all != "" {
		if all != "on" && all != "off" {
			return fmt.Errorf("invalid -all %q. Use 'on' or 'off'", all)
		}
		return a.setAndNotify(a.ast.SetStatus(all == "on"))
	}
	if recipient != "" {
		if set != "on" && set != "off" {
			return fmt.Errorf("usage: clark toggle -r <name|number> -set on|off")
		}
		return a.setAndNotify(a.ast.SetVIPStatus(recipient, set == "on"))
	}
	if set != "" {
		return fmt.Errorf("usage: clark toggle -set requires -r <name|number>")
	}

	return a.setAndNotify(a.ast.Toggle())
}

// Think enables or disables the model's reasoning mode.
func (a *App) Think(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return fmt.Errorf("usage: clark think on|off")
	}

	return a.setAndNotify(a.ast.SetThinking(args[0] == "on"))
}

// History sets how many recent messages clark reviews on every turn.
func (a *App) History(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	if len(args) != 1 {
		return fmt.Errorf("usage: clark history <N>")
	}
	limit, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid history limit %q", args[0])
	}

	return a.setAndNotify(a.ast.SetHistoryLimit(limit))
}

// View prints the current settings, VIP list, and per-VIP tool access.
func (a *App) View() error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	logging.Log("CLARK", logging.SevInfo, "VIEW", "Settings",
		"name", a.ast.Name(),
		"model", a.ast.Model(),
		"status", a.ast.Enabled(),
		"thinking", a.ast.Thinking(),
		"history", a.ast.HistoryLimit(),
		"context", a.ast.Context())
	logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "Current VIP list")
	for jid, name := range a.ast.VIPList() {
		grants, _, _ := a.ast.AccessFor(jid)
		logging.Log("MEMORY", logging.SevInfo, "VIPLIST", "VIP entry", "jid", jid, "relation", name, "status", a.ast.EnabledFor(jid), "tools", grants)
	}

	return nil
}

// Access manages a VIP's granted tools.
func (a *App) Access(args []string) error {
	available, err := a.ast.IsInitialized()
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	fs := flag.NewFlagSet("access", flag.ContinueOnError)
	var setAccess string
	var tool string
	var onOff string

	fs.StringVar(&setAccess, "recipient", "", "VIP name or number")
	fs.StringVar(&setAccess, "r", "", "VIP name or number (shorthand)")
	fs.StringVar(&tool, "tool", "", "Tool to grant or revoke, e.g. web_search")
	fs.StringVar(&onOff, "set", "", "on or off")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing args: %w", err)
	}

	if setAccess == "" && tool == "" && onOff == "" {
		fs.Usage()
		return fmt.Errorf("empty input. Use: clark access -r <name|number> -tool <tool> -set on|off")
	}

	enabled := onOff == "on"
	if onOff != "" && onOff != "on" && onOff != "off" {
		return fmt.Errorf("invalid -set %q. Use 'on' or 'off'", onOff)
	}

	jid, ok := a.ast.LookupJID(setAccess)
	if !ok {
		return fmt.Errorf("no VIP found matching %q", setAccess)
	}

	grants, _, err := a.ast.AccessFor(jid)
	if err != nil {
		return err
	}

	if enabled {
		found := false
		for _, g := range grants {
			if g == tool {
				found = true
				break
			}
		}
		if !found {
			grants = append(grants, tool)
		}
	} else {
		next := grants[:0]
		for _, g := range grants {
			if g != tool {
				next = append(next, g)
			}
		}
		grants = next
	}

	if err := a.ast.SetAccess(jid, grants); err != nil {
		return err
	}
	a.notifyRunning()

	logging.Log("MEMORY", logging.SevInfo, "ACCESS", "Access updated",
		"jid", jid, "tool", tool, "enabled", enabled, "grants", grants)
	return nil
}

// Help prints the CLI usage.
func (a *App) Help() error {
	logging.Log("CLARK", logging.SevInfo, "HELP", "clark commands",
		"init", "clark init",
		"run", "clark run",
		"vip", "clark vip -a <number,name,relation> | -d <number> | -clear",
		"ctx", "clark ctx -c <context> | -clear",
		"toggle", "clark toggle | -r <name|number> -set on|off | -all on|off",
		"think", "clark think on|off",
		"history", "clark history <N>",
		"view", "clark view",
		"access", "clark access -r <name|number> -tool <tool> -set on|off")
	return nil
}

// pidPath returns the pidfile location: a fixed name next to the store so both
// `clark run` and the `clark` CLI (which resolve the same cfg.DBPath) agree on
// where to write/read the running process's PID.
func pidPath(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(cfg.DBPath), "clark.pid")
}

func writePidFile(cfg *config.Config) error {
	return os.WriteFile(pidPath(cfg), []byte(strconv.Itoa(os.Getpid())), 0o600)
}

func removePidFile(cfg *config.Config) {
	_ = os.Remove(pidPath(cfg))
}

// setAndNotify applies a setting change and, on success, pokes any running
// `clark run` process to reload its cache from the DB so the change takes
// effect live (the two are separate processes sharing one SQLite store).
func (a *App) setAndNotify(err error) error {
	if err != nil {
		return err
	}
	a.notifyRunning()
	return nil
}

// notifyRunning signals a running `clark run` process (if any) with SIGHUP so it
// reloads its in-memory cache from the DB. Best-effort: missing or stale pidfile
// (no instance running, or a dead PID) is silently ignored.
func (a *App) notifyRunning() {
	data, err := os.ReadFile(pidPath(a.cfg))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Signal(syscall.SIGHUP)
}
