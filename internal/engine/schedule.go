package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func init() {
	register(&Method{Name: "schedule.message", Summary: "Send a message later (the daemon must be running at that time)", Params: api.ScheduleParams{}, Mutating: true, H: mSchedule})
	register(&Method{Name: "schedule.list", Summary: "Scheduled messages", Params: api.ScheduleListParams{}, H: mScheduleList})
	register(&Method{Name: "schedule.cancel", Summary: "Cancel a scheduled message", Params: api.ScheduleRef{}, Mutating: true, H: mScheduleCancel})
	register(&Method{Name: "audit.verify", Summary: "Check the audit log's hash chain for tampering", Params: api.Empty{}, H: mAuditVerify})
}

// lateGrace is how late a scheduled message may still go out after the daemon was down.
const lateGrace = time.Hour

var clockRe = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)

// ParseWhen reads "in" durations and human times in local time. It never returns a time in the past.
func ParseWhen(at, in string, now time.Time) (time.Time, error) {
	if in = strings.TrimSpace(in); in != "" {
		d, err := config.ParseDuration(strings.TrimPrefix(in, "+"))
		if err != nil || d <= 0 {
			return time.Time{}, fmt.Errorf("can't read delay %q (use e.g. 45m, 2h, 1d)", in)
		}
		return now.Add(d), nil
	}
	s := strings.ToLower(strings.TrimSpace(at))
	if s == "" {
		return time.Time{}, fmt.Errorf("give a time (at) or a delay (in)")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02 3:04pm", "2006-01-02 3pm"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	day := now
	switch {
	case strings.HasPrefix(s, "tomorrow"):
		day, s = now.AddDate(0, 0, 1), strings.TrimSpace(strings.TrimPrefix(s, "tomorrow"))
	case strings.HasPrefix(s, "today"):
		s = strings.TrimSpace(strings.TrimPrefix(s, "today"))
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "at "))
	m := clockRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, fmt.Errorf("can't read time %q (try 2026-11-20 09:00, 9am, 21:30, tomorrow 9:30am, or in=2h)", at)
	}
	h, _ := strconv.Atoi(m[1])
	min := 0
	if m[2] != "" {
		min, _ = strconv.Atoi(m[2])
	}
	switch m[3] {
	case "pm":
		if h < 12 {
			h += 12
		}
	case "am":
		if h == 12 {
			h = 0
		}
	}
	if h > 23 || min > 59 {
		return time.Time{}, fmt.Errorf("bad time %q", at)
	}
	t := time.Date(day.Year(), day.Month(), day.Day(), h, min, 0, 0, now.Location())
	if !t.After(now) && !strings.HasPrefix(strings.ToLower(at), "today") && !strings.HasPrefix(strings.ToLower(at), "tomorrow") {
		t = t.AddDate(0, 0, 1) // "9am" after 9am means tomorrow
	}
	return t, nil
}

func mSchedule(c *Call) (any, error) {
	var p api.ScheduleParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	due, err := ParseWhen(p.At, p.In, time.Now())
	if err != nil {
		return nil, usage("%v", err)
	}
	if time.Until(due) < 5*time.Second {
		return nil, usage("that time is in the past (%s)", due.Format("Mon 15:04"))
	}
	if time.Until(due) > 366*24*time.Hour {
		return nil, usage("schedules can be at most a year ahead")
	}
	if p.Text == "" && p.File == "" && p.Location == nil && p.ContactCard == nil {
		return nil, usage("nothing to send: give text, a file, a location or a contact")
	}
	if err := c.checkReadable(p.File); err != nil {
		return nil, err
	}
	to, _, err := c.E.resolve(c.Ctx, p.To)
	if err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("%s to %s at %s", describeSend(&p.SendParams), c.E.displayName(c.Ctx, to), due.Local().Format("Mon Jan 2 15:04"))
	// Approval (if any) happens now, so the send itself can go out unattended.
	dry, err := c.Authorize(Action{Targets: []types.JID{to}, Messages: true, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, to, summary)
	}
	send := p.SendParams
	send.To = to.String()
	raw, _ := json.Marshal(send)
	s := &store.Scheduled{Due: due, Source: c.Req.Meta.Source, Profile: c.profile, Method: "send.message", Params: string(raw), Summary: summary}
	if err := c.E.DB.CreateScheduled(c.Ctx, s); err != nil {
		return nil, err
	}
	return c.E.toAPIScheduled(s), nil
}

func (e *Engine) toAPIScheduled(s *store.Scheduled) api.Scheduled {
	out := api.Scheduled{ID: s.ID, Due: s.Due.Format(time.RFC3339), Status: s.Status, Summary: s.Summary, Source: s.Source, Created: s.Created.Format(time.RFC3339), Error: s.Error}
	var p api.SendParams
	if json.Unmarshal([]byte(s.Params), &p) == nil {
		out.To = p.To
	}
	if !s.SentAt.IsZero() {
		out.SentAt = s.SentAt.Format(time.RFC3339)
	}
	if s.Result != "" {
		out.Result = json.RawMessage(s.Result)
	}
	return out
}

func mScheduleList(c *Call) (any, error) {
	var p api.ScheduleListParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if p.Status == "" {
		p.Status = "pending"
	}
	rows, err := c.E.DB.ListScheduled(c.Ctx, p.Status, 200)
	if err != nil {
		return nil, err
	}
	out := []api.Scheduled{}
	for _, s := range rows {
		out = append(out, c.E.toAPIScheduled(s))
	}
	return out, nil
}

func mScheduleCancel(c *Call) (any, error) {
	var p api.ScheduleRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	s, err := c.E.DB.GetScheduled(c.Ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, notFound("no scheduled message #%d", p.ID)
	}
	if _, err := c.Authorize(Action{Summary: "cancel " + s.Summary}); err != nil {
		return nil, err
	}
	ok, err := c.E.DB.CancelScheduled(c.Ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, api.Errf(api.CodeUsage, "#%d is already %s", p.ID, s.Status)
	}
	return map[string]any{"id": p.ID, "status": "cancelled"}, nil
}

// scheduler sends due messages. Items more than an hour late (daemon was off) are marked missed.
func (e *Engine) scheduler() {
	defer e.wg.Done()
	e.DB.RecoverSending(e.ctx)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
		}
		due, err := e.DB.ClaimDue(e.ctx, time.Now())
		if err != nil {
			continue
		}
		for _, s := range due {
			if time.Since(s.Due) > lateGrace {
				_ = e.DB.FinishScheduled(e.ctx, s.ID, "missed", "", fmt.Sprintf("the daemon wasn't running at %s", s.Due.Local().Format("Jan 2 15:04")))
				e.publish(api.Event{Type: "scheduled.missed", Data: map[string]any{"id": s.ID, "summary": s.Summary}})
				continue
			}
			res, err := e.Dispatch(e.ctx, &api.Request{Method: s.Method, Params: json.RawMessage(s.Params), Meta: api.Meta{Source: "schedule", Profile: s.Profile, Yes: true}}, nil)
			status, result := "sent", ""
			if err != nil {
				status = "failed"
			} else if b, err := json.Marshal(res); err == nil {
				result = string(b)
			}
			_ = e.DB.FinishScheduled(e.ctx, s.ID, status, result, errString(err))
			e.publish(api.Event{Type: "scheduled." + status, Data: map[string]any{"id": s.ID, "summary": s.Summary, "error": errString(err)}})
		}
	}
}

func mAuditVerify(c *Call) (any, error) {
	n, bad, err := c.E.DB.VerifyAudit(c.Ctx)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"rows": n, "intact": bad == 0}
	if bad != 0 {
		res["first_bad_row"] = bad
	}
	return res, nil
}
