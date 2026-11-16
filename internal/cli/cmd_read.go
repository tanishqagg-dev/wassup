package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tanishqagg-dev/wassup/internal/api"
)

func cmdMessages(app *App) *cobra.Command {
	var p api.ListMessagesParams
	var mine, theirs, desc bool
	cmd := &cobra.Command{
		Use:     "messages [chat]",
		Aliases: []string{"msgs", "log"},
		Short:   "Show messages in a chat (or across all chats)",
		Example: `  wassup messages "Mom"
  wassup messages "Team" -n 100 --after 2d
  wassup messages --unread "Alex"
  wassup messages --type image "Family" --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				p.Chat = args[0]
			}
			if mine {
				t := true
				p.FromMe = &t
			} else if theirs {
				f := false
				p.FromMe = &f
			}
			if desc {
				p.Order = "desc"
			}
			var msgs []api.Message
			if err := app.call("messages.list", p, &msgs); err != nil {
				return err
			}
			if msgs == nil {
				msgs = []api.Message{}
			}
			return app.emit(msgs, func() { app.printMessages(msgs, p.Chat == "") })
		},
	}
	f := cmd.Flags()
	f.IntVarP(&p.Limit, "limit", "n", 30, "how many messages")
	f.StringVar(&p.Before, "before", "", "only before a time (RFC3339, YYYY-MM-DD, 2h, 3d) or message ID")
	f.StringVar(&p.After, "after", "", "only after a time or message ID")
	f.StringVar(&p.From, "from", "", "only from this sender")
	f.BoolVar(&mine, "mine", false, "only your messages")
	f.BoolVar(&theirs, "theirs", false, "only other people's messages")
	f.StringVar(&p.Type, "type", "", "text, image, video, audio, voice, document, sticker, location, contact, poll")
	f.BoolVar(&p.Unread, "unread", false, "only messages since you last read the chat")
	f.BoolVar(&desc, "desc", false, "newest first")
	return cmd
}

func cmdMessage(app *App) *cobra.Command {
	var chat string
	cmd := &cobra.Command{
		Use:     "message <message-id>",
		Aliases: []string{"show"},
		Short:   "Show one message with its reactions",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var m api.Message
			if err := app.call("messages.get", api.MessageRef{Chat: chat, ID: args[0]}, &m); err != nil {
				return err
			}
			return app.emit(m, func() {
				app.printMessage(m, true)
				for _, r := range m.Reactions {
					fmt.Fprintf(app.out, "    %s %s\n", r.Emoji, r.SenderName)
				}
				if m.Media != nil && m.Media.Path != "" {
					fmt.Fprintf(app.out, "    saved at %s\n", m.Media.Path)
				}
			})
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	return cmd
}

func cmdSearch(app *App) *cobra.Command {
	var p api.SearchParams
	cmd := &cobra.Command{
		Use:   "search <words...>",
		Short: "Full-text search across your stored messages",
		Example: `  wassup search invoice
  wassup search "flight number" --chat "Mom"
  wassup search pass* --after 30d`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p.Query = strings.Join(args, " ")
			var msgs []api.Message
			if err := app.call("messages.search", p, &msgs); err != nil {
				return err
			}
			if msgs == nil {
				msgs = []api.Message{}
			}
			return app.emit(msgs, func() { app.printMessages(msgs, true) })
		},
	}
	f := cmd.Flags()
	f.StringVar(&p.Chat, "chat", "", "only in this chat")
	f.StringVar(&p.From, "from", "", "only from this sender")
	f.StringVar(&p.Type, "type", "", "only this message type")
	f.StringVar(&p.After, "after", "", "only after this time")
	f.StringVar(&p.Before, "before", "", "only before this time")
	f.IntVarP(&p.Limit, "limit", "n", 30, "max results")
	return cmd
}

func cmdWait(app *App) *cobra.Command {
	var p api.WaitParams
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Block until a matching message arrives, then print it",
		Long: `Waits for the next incoming message that matches. Exits 0 with the message,
or exit code 7 on timeout. Built for scripts and agents that need a reply.`,
		Example: `  wassup wait --chat "Alex" --timeout 10m
  wassup wait --from +14155550123 --match "^(yes|no)$"
  wassup wait --count 3 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p.Timeout = timeout.String()
			onEvent := func(raw json.RawMessage) error {
				if app.JSON {
					fmt.Fprintln(app.out, string(raw))
					return nil
				}
				var m api.Message
				if json.Unmarshal(raw, &m) == nil {
					app.printMessage(m, true)
				}
				return nil
			}
			var raw json.RawMessage
			if err := app.callStream("messages.wait", p, &raw, onEvent, timeout+15*time.Second); err != nil {
				return err
			}
			if p.Count > 1 {
				if app.JSON {
					return app.emit(raw, nil)
				}
				return nil
			}
			var m api.Message
			_ = json.Unmarshal(raw, &m)
			return app.emit(m, func() { app.printMessage(m, true) })
		},
	}
	f := cmd.Flags()
	f.StringVar(&p.Chat, "chat", "", "only messages in this chat")
	f.StringVar(&p.From, "from", "", "only messages from this person")
	f.StringVar(&p.Match, "match", "", "regular expression the text must match (case-insensitive)")
	f.DurationVar(&timeout, "timeout", 5*time.Minute, "how long to wait")
	f.BoolVar(&p.IncludeFromMe, "include-mine", false, "also match messages you send from your phone")
	f.IntVar(&p.Count, "count", 1, "wait for this many messages")
	return cmd
}

func cmdHistory(app *App) *cobra.Command {
	var p api.HistoryParams
	cmd := &cobra.Command{
		Use:   "history <chat>",
		Short: "Fetch older messages for a chat from your phone",
		Long:  "Asks your phone to send messages older than the oldest one stored. The phone must be online.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p.Chat = args[0]
			var res map[string]any
			if err := app.callStream("history.request", p, &res, nil, 2*time.Minute); err != nil {
				return err
			}
			return app.emit(res, func() {
				app.ok("received %v older messages", res["received"])
				if n, ok := res["note"]; ok {
					fmt.Fprintln(app.out, n)
				}
			})
		},
	}
	cmd.Flags().IntVarP(&p.Count, "count", "n", 50, "how many messages to ask for (max 500)")
	cmd.Flags().StringVar(&p.Timeout, "wait", "30s", "how long to wait for the phone")
	return cmd
}

func cmdRead(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "read <chat> [message-id...]",
		Short: "Mark a chat (or specific messages) as read, sending blue ticks",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("messages.read", api.MarkReadParams{Chat: args[0], IDs: args[1:]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("marked %v messages read", res["marked"]) })
		},
	}
}
