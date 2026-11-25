package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/willis-dev-md/data-importer/internal/schedule"
	"github.com/willis-dev-md/data-importer/internal/store"
)

// runSchedule implements `importer schedule list`: one line per source with its schedule, next run and last run
// status. It only reads the database, so it works with or without `serve`. Exit codes: 0 ok, 1 database error,
// 2 usage (or invalid DB_* env or missing DATABASE_URL).
func runSchedule(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: importer schedule list")
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, closeDB, code := openStore(ctx, "schedule", stderr)
	if code != exitOK {
		return code
	}
	defer closeDB()
	rows, err := st.ListSchedules(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "importer schedule list: %v\n", err)
		return exitFailed
	}
	writeScheduleList(stdout, rows)
	return exitOK
}

func writeScheduleList(w io.Writer, rows []store.ScheduleRow) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tENABLED\tSCHEDULE\tTIMEZONE\tNEXT_RUN_AT\tLAST_SCHEDULED_AT\tLAST_RUN")
	for _, r := range rows {
		sum, tz := "manual", "-"
		if r.Schedule != nil {
			sum, tz = summarizeSchedule(*r.Schedule), r.Schedule.Timezone
		}
		last := "-"
		if r.LastRunStatus != "" {
			last = r.LastRunStatus
			if r.LastRunTrigger != "" {
				last += " (" + r.LastRunTrigger + ")"
			}
		}
		fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Enabled, sum, tz, fmtTime(r.NextRunAt), fmtTime(r.LastScheduledAt), last)
	}
	_ = tw.Flush()
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

// summarizeSchedule is the one-line form of a schedule, for example "weekly on monday at 06:00".
func summarizeSchedule(s schedule.Spec) string {
	var b strings.Builder
	b.WriteString(s.Every)
	switch s.Every {
	case schedule.Weekly:
		b.WriteString(" on " + s.DayOfWeek)
	case schedule.Monthly:
		if s.DayOfMonth == schedule.LastDay {
			b.WriteString(" on the last day")
		} else {
			fmt.Fprintf(&b, " on day %d", s.DayOfMonth)
		}
	}
	b.WriteString(" at " + s.At)
	if s.Jitter != "" && s.Jitter != "0s" {
		b.WriteString(", jitter " + s.Jitter)
	}
	if s.OnMissed == schedule.Skip {
		b.WriteString(", skip missed")
	}
	return b.String()
}
