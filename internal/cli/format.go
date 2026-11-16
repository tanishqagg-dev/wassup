package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tanishqagg-dev/wassup/internal/api"
)

func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	now := time.Now()
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	case t.Year() == now.Year():
		return t.Format("Jan 2 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

func whenp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return when(*t)
}

func table(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0) }

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	case n > 0:
		return fmt.Sprintf("%d B", n)
	}
	return ""
}

// printMessages renders a conversation the way you'd read it on a phone.
func (a *App) printMessages(msgs []api.Message, showChat bool) {
	if len(msgs) == 0 {
		fmt.Fprintln(a.out, a.paint("2", "no messages"))
		return
	}
	for _, m := range msgs {
		a.printMessage(m, showChat)
	}
}

func (a *App) printMessage(m api.Message, showChat bool) {
	who := m.SenderName
	if m.FromMe {
		who = "me"
	}
	if who == "" {
		who = m.Sender
	}
	head := a.paint("2", when(m.Time))
	if showChat && m.ChatName != "" && m.ChatName != who {
		head += " " + a.paint("36", "["+m.ChatName+"]")
	}
	nameColor := "1;32"
	if m.FromMe {
		nameColor = "1;34"
	}
	head += " " + a.paint(nameColor, who)
	var body strings.Builder
	if m.Revoked {
		body.WriteString(a.paint("2", "(deleted)"))
	} else {
		if m.Type != "text" {
			label := m.Type
			if m.Media != nil {
				var bits []string
				if m.Media.FileName != "" {
					bits = append(bits, m.Media.FileName)
				}
				if m.Media.Seconds > 0 {
					bits = append(bits, fmt.Sprintf("%ds", m.Media.Seconds))
				}
				if s := humanSize(m.Media.Size); s != "" {
					bits = append(bits, s)
				}
				if len(bits) > 0 {
					label += ": " + strings.Join(bits, ", ")
				}
			}
			if m.Location != nil {
				label = fmt.Sprintf("location %.5f,%.5f", m.Location.Lat, m.Location.Lng)
			}
			body.WriteString(a.paint("35", "["+label+"]"))
			if m.Text != "" {
				body.WriteString(" ")
			}
		}
		if m.Poll != nil {
			body.WriteString(m.Poll.Question)
			for _, o := range m.Poll.Options {
				body.WriteString("\n      ( ) " + o.Name)
			}
		} else {
			body.WriteString(m.Text)
		}
		if m.Edited {
			body.WriteString(a.paint("2", " (edited)"))
		}
	}
	extras := ""
	if m.QuotedID != "" {
		extras += a.paint("2", " ↩ "+m.QuotedID)
	}
	if m.FromMe && m.Status != "" {
		extras += a.paint("2", " · "+m.Status)
	}
	if len(m.Reactions) > 0 {
		var rs []string
		for _, r := range m.Reactions {
			rs = append(rs, r.Emoji)
		}
		extras += " " + strings.Join(rs, "")
	}
	fmt.Fprintf(a.out, "%s %s\n    %s%s\n", head, a.paint("2", m.ID), strings.ReplaceAll(body.String(), "\n", "\n    "), extras)
}

func (a *App) printChats(chats []api.Chat) {
	if len(chats) == 0 {
		fmt.Fprintln(a.out, a.paint("2", "no chats yet (history may still be syncing; try again in a minute)"))
		return
	}
	tw := table(a.out)
	fmt.Fprintln(tw, "NAME\tUNREAD\tLAST\tKIND\tJID\tPREVIEW")
	for _, c := range chats {
		flags := ""
		if c.Pinned {
			flags += "*"
		}
		if c.MutedUntil != nil {
			flags += "~"
		}
		unread := ""
		if c.Unread > 0 {
			unread = fmt.Sprint(c.Unread)
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\t%s\n", trunc(c.Name, 28), flags, unread, whenp(c.LastMessageAt), c.Kind, c.JID, trunc(c.LastMessage, 50))
	}
	tw.Flush()
}

func (a *App) kv(rows ...[2]string) {
	tw := table(a.out)
	for _, r := range rows {
		if r[1] == "" {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\n", a.paint("2", r[0]), r[1])
	}
	tw.Flush()
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (a *App) ok(format string, args ...any) {
	fmt.Fprintf(a.out, "%s %s\n", a.paint("32", "ok"), fmt.Sprintf(format, args...))
}

func readStdin() (string, error) {
	st, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("--stdin given but nothing is piped in")
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}
