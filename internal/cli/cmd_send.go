package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tanishqagg-dev/wassup/internal/api"
)

type sendFlags struct {
	file, as, fileName, replyTo, location, place, address string
	contactName, contactPhone, vcardFile                  string
	mentions                                              []string
	mentionAll, viewOnce, noPreview, stdin, typing        bool
	wait                                                  time.Duration
	at, in                                                string
}

func (f *sendFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVarP(&f.file, "file", "f", "", "attach a file (image, video, audio, document...)")
	fl.StringVar(&f.as, "as", "", "send the file as: image, video, gif, audio, voice, document, sticker")
	fl.StringVar(&f.fileName, "filename", "", "file name shown for documents")
	fl.StringVarP(&f.replyTo, "reply", "r", "", "quote and reply to this message ID")
	fl.StringSliceVarP(&f.mentions, "mention", "m", nil, "@mention people (repeatable; phones, names or JIDs)")
	fl.BoolVar(&f.mentionAll, "mention-all", false, "notify every member of the group")
	fl.BoolVar(&f.viewOnce, "view-once", false, "send media as view once")
	fl.BoolVar(&f.noPreview, "no-preview", false, "don't attach a link preview")
	fl.StringVar(&f.location, "location", "", "send a map pin: \"lat,lng\"")
	fl.StringVar(&f.place, "place", "", "place name for --location")
	fl.StringVar(&f.address, "address", "", "address for --location")
	fl.StringVar(&f.contactName, "contact-name", "", "send a contact card with this name")
	fl.StringVar(&f.contactPhone, "contact-phone", "", "phone number for the contact card")
	fl.StringVar(&f.vcardFile, "vcard", "", "send a contact card from a .vcf file")
	fl.BoolVar(&f.stdin, "stdin", false, "read the message text from stdin")
	fl.BoolVar(&f.typing, "typing", false, "show typing... for a moment first")
	fl.DurationVar(&f.wait, "wait", 0, "after sending, wait this long for a reply and print it")
	fl.StringVar(&f.at, "at", "", "send later at this time: 9am, 21:30, \"tomorrow 9:15am\", 2026-12-01 08:00")
	fl.StringVar(&f.in, "in", "", "send later after this delay: 45m, 2h, 1d")
}

func (f *sendFlags) params(to string, words []string) (*api.SendParams, error) {
	text := strings.Join(words, " ")
	if f.stdin || text == "-" {
		s, err := readStdin()
		if err != nil {
			return nil, err
		}
		text = s
	}
	p := &api.SendParams{To: to, Text: text, File: f.file, As: f.as, FileName: f.fileName, ReplyTo: f.replyTo,
		Mentions: f.mentions, MentionAll: f.mentionAll, ViewOnce: f.viewOnce, NoPreview: f.noPreview, Typing: f.typing}
	if f.location != "" {
		parts := strings.Split(f.location, ",")
		if len(parts) != 2 {
			return nil, fmt.Errorf("--location must be \"lat,lng\"")
		}
		lat, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		lng, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("--location must be \"lat,lng\" with numbers")
		}
		p.Location = &api.LocationInput{Lat: lat, Lng: lng, Name: f.place, Address: f.address}
	}
	if f.vcardFile != "" {
		b, err := os.ReadFile(f.vcardFile)
		if err != nil {
			return nil, err
		}
		name := f.contactName
		if name == "" {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "FN:") {
					name = strings.TrimSpace(strings.TrimPrefix(line, "FN:"))
				}
			}
		}
		p.ContactCard = &api.ContactInput{Name: name, VCard: string(b)}
	} else if f.contactName != "" {
		p.ContactCard = &api.ContactInput{Name: f.contactName, Phone: f.contactPhone}
	}
	return p, nil
}

func (a *App) printSent(r api.SendResult) {
	a.ok("sent %s to %s  %s", r.Type, r.ChatName, a.paint("2", r.ID))
}

// sendAndMaybeWait sends, then optionally blocks for the first reply in the same chat.
func (a *App) scheduleSend(p *api.SendParams, at, in string) error {
	var res api.Scheduled
	if err := a.call("schedule.message", api.ScheduleParams{SendParams: *p, At: at, In: in}, &res); err != nil {
		return err
	}
	return a.emit(res, func() {
		t, _ := time.Parse(time.RFC3339, res.Due)
		a.ok("scheduled #%d for %s", res.ID, t.Local().Format("Mon Jan 2 15:04"))
		fmt.Fprintln(a.out, a.paint("2", "the daemon must be running then; see wassup schedule, cancel with wassup schedule cancel "+fmt.Sprint(res.ID)))
	})
}

func (a *App) sendAndMaybeWait(p *api.SendParams, wait time.Duration) error {
	var res api.SendResult
	if err := a.call("send.message", p, &res); err != nil {
		return err
	}
	if wait <= 0 || res.ID == "" {
		return a.emit(res, func() { a.printSent(res) })
	}
	if !a.JSON {
		a.printSent(res)
		fmt.Fprintf(a.out, "%s\n", a.paint("2", "waiting for a reply..."))
	}
	var reply api.Message
	err := a.callStream("messages.wait", api.WaitParams{Chat: res.Chat, Timeout: wait.String()}, &reply, nil, wait+10*time.Second)
	if err != nil {
		return err
	}
	return a.emit(map[string]any{"sent": res, "reply": reply}, func() { a.printMessage(reply, false) })
}

func cmdSend(app *App) *cobra.Command {
	var f sendFlags
	var voice bool
	cmd := &cobra.Command{
		Use:   "send <to> [text...]",
		Short: "Send a message, file, voice note, location or contact",
		Long: `Send to a phone number (+country code), a contact or group name, a JID, or "me".
Names must match exactly one chat; if not, wassup lists the candidates instead of guessing.`,
		Example: `  wassup send +14155550123 "on my way"
  wassup send "Mom" "landed safely"
  wassup send "Family" -f ~/Pictures/beach.jpg "look at this"
  wassup send "Rahul" -f memo.m4a --voice
  wassup send "Team" "standup in 5 @14155550123"
  wassup send me "note to self"
  echo "long text" | wassup send Alex --stdin
  wassup send Alex "you there?" --wait 2m
  wassup send Mom "Happy birthday!" --at "tomorrow 9am"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := f.params(args[0], args[1:])
			if err != nil {
				return app.fail(api.Errf(api.CodeUsage, "%v", err))
			}
			if voice {
				p.As = "voice"
			}
			if f.at != "" || f.in != "" {
				return app.scheduleSend(p, f.at, f.in)
			}
			return app.sendAndMaybeWait(p, f.wait)
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&voice, "voice", false, "send the --file as a voice note (converted to Opus if needed)")
	return cmd
}

func cmdSendMany(app *App) *cobra.Command {
	var to []string
	var toFile, file, as, delay string
	var stdin bool
	cmd := &cobra.Command{
		Use:   "send-many [text...]",
		Short: "Send one message to several chats, paced to stay under spam limits",
		Example: `  wassup send-many --to "Alice" --to "Bob" "dinner at 8?"
  wassup send-many --to-file guests.txt -f invite.pdf "You're invited"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if toFile != "" {
				fh, err := os.Open(toFile)
				if err != nil {
					return app.fail(api.Errf(api.CodeUsage, "%v", err))
				}
				sc := bufio.NewScanner(fh)
				for sc.Scan() {
					if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
						to = append(to, line)
					}
				}
				fh.Close()
			}
			text := strings.Join(args, " ")
			if stdin {
				s, err := readStdin()
				if err != nil {
					return app.fail(api.Errf(api.CodeUsage, "%v", err))
				}
				text = s
			}
			if len(to) == 0 {
				return app.fail(api.Errf(api.CodeUsage, "give recipients with --to or --to-file"))
			}
			var res api.SendManyResult
			if err := app.callStream("send.many", api.SendManyParams{To: to, Text: text, File: file, As: as, Delay: delay}, &res, nil, 0); err != nil {
				return err
			}
			if err := app.emit(res, func() {
				for _, s := range res.Sent {
					app.printSent(s)
				}
				for _, f := range res.Failed {
					fmt.Fprintf(app.out, "%s %s: %s\n", app.paint("31", "failed"), f.To, f.Error.Message)
				}
			}); err != nil {
				return err
			}
			if len(res.Failed) > 0 {
				return exitError{1}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&to, "to", nil, "recipient (repeatable, or comma-separated)")
	cmd.Flags().StringVar(&toFile, "to-file", "", "file with one recipient per line")
	cmd.Flags().StringVarP(&file, "file", "f", "", "attach a file")
	cmd.Flags().StringVar(&as, "as", "", "send the file as: image, video, audio, voice, document, sticker")
	cmd.Flags().StringVar(&delay, "delay", "", "pause between recipients (default 2s plus jitter)")
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read the text from stdin")
	return cmd
}

func cmdReply(app *App) *cobra.Command {
	var f sendFlags
	var chat string
	cmd := &cobra.Command{
		Use:     "reply <message-id> [text...]",
		Short:   "Reply to a message, quoting it",
		Example: "  wassup reply 3EB0C431D5F7A1B2C3D4 \"sounds good\"",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var m api.Message
			if err := app.call("messages.get", api.MessageRef{Chat: chat, ID: args[0]}, &m); err != nil {
				return err
			}
			p, err := f.params(m.Chat, args[1:])
			if err != nil {
				return app.fail(api.Errf(api.CodeUsage, "%v", err))
			}
			p.ReplyTo = m.ID
			return app.sendAndMaybeWait(p, f.wait)
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	return cmd
}

func cmdReact(app *App) *cobra.Command {
	var chat string
	cmd := &cobra.Command{
		Use:     "react <message-id> <emoji>",
		Short:   "React to a message",
		Example: "  wassup react 3EB0C431D5F7A1B2C3D4 👍",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("messages.react", api.ReactParams{Chat: chat, ID: args[0], Emoji: args[1]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("reacted %s", args[1]) })
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	return cmd
}

func cmdUnreact(app *App) *cobra.Command {
	var chat string
	cmd := &cobra.Command{
		Use:   "unreact <message-id>",
		Short: "Remove your reaction from a message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("messages.react", api.ReactParams{Chat: chat, ID: args[0]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("reaction removed") })
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	return cmd
}

func cmdEdit(app *App) *cobra.Command {
	var chat string
	cmd := &cobra.Command{
		Use:   "edit <message-id> <new text...>",
		Short: "Edit one of your messages (within 15 minutes)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("messages.edit", api.EditParams{Chat: chat, ID: args[0], Text: strings.Join(args[1:], " ")}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("edited") })
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	return cmd
}

func cmdDelete(app *App) *cobra.Command {
	var chat string
	var local bool
	cmd := &cobra.Command{
		Use:     "delete <message-id>",
		Aliases: []string{"unsend", "revoke"},
		Short:   "Delete a message for everyone",
		Long:    "Deletes your own message for everyone, or someone else's message in a group where you're an admin.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("messages.delete", api.DeleteParams{Chat: chat, ID: args[0], LocalOnly: local}, &res); err != nil {
				return err
			}
			return app.emit(res, func() {
				if local {
					app.ok("removed from the local store")
				} else {
					app.ok("deleted for everyone")
				}
			})
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	cmd.Flags().BoolVar(&local, "local", false, "only remove the local copy; nothing is sent")
	return cmd
}

func cmdForward(app *App) *cobra.Command {
	var chat string
	cmd := &cobra.Command{
		Use:   "forward <message-id> <to>",
		Short: "Forward a message to another chat",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res api.SendResult
			if err := app.call("messages.forward", api.ForwardParams{Chat: chat, ID: args[0], To: args[1]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("forwarded to %s  %s", res.ChatName, app.paint("2", res.ID)) })
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the original message, if the ID isn't unique")
	return cmd
}

func cmdPin(app *App) *cobra.Command {
	var chat, dur string
	var unpin bool
	cmd := &cobra.Command{
		Use:   "pin <message-id>",
		Short: "Pin a message at the top of its chat",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("messages.pin", api.PinParams{Chat: chat, ID: args[0], Unpin: unpin, Duration: dur}, &res); err != nil {
				return err
			}
			return app.emit(res, func() {
				if unpin {
					app.ok("unpinned")
				} else {
					app.ok("pinned")
				}
			})
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	cmd.Flags().StringVar(&dur, "for", "", "how long: 24h, 7d (default) or 30d")
	cmd.Flags().BoolVar(&unpin, "unpin", false, "unpin instead")
	return cmd
}

func cmdStar(app *App) *cobra.Command {
	var chat string
	var unstar bool
	cmd := &cobra.Command{
		Use:   "star <message-id>",
		Short: "Star a message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("messages.star", api.StarParams{Chat: chat, ID: args[0], Unstar: unstar}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("starred: %v", !unstar) })
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	cmd.Flags().BoolVar(&unstar, "unstar", false, "remove the star")
	return cmd
}

func cmdPoll(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "poll", Short: "Create polls, vote, and see results"}
	var multi bool
	create := &cobra.Command{
		Use:     "create <to> <question> <option> <option> [option...]",
		Short:   "Send a poll",
		Example: `  wassup poll create "Team" "Lunch?" Pizza Sushi Tacos --multi`,
		Args:    cobra.MinimumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			sel := 1
			if multi {
				sel = -1
			}
			var res api.SendResult
			if err := app.call("polls.create", api.PollCreateParams{To: args[0], Question: args[1], Options: args[2:], Selectable: sel}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.printSent(res) })
		},
	}
	create.Flags().BoolVar(&multi, "multi", false, "allow picking several options")
	var chat string
	vote := &cobra.Command{
		Use:   "vote <poll-message-id> [option...]",
		Short: "Vote (no options clears your vote)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			opts := args[1:]
			if opts == nil {
				opts = []string{}
			}
			if err := app.call("polls.vote", api.PollVoteParams{Chat: chat, ID: args[0], Options: opts}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("voted %s", strings.Join(opts, ", ")) })
		},
	}
	vote.Flags().StringVar(&chat, "chat", "", "chat of the poll")
	results := &cobra.Command{
		Use:   "results <poll-message-id>",
		Short: "Show votes so far",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res struct {
				Question string           `json:"question"`
				Options  []api.PollOption `json:"options"`
				Voters   int              `json:"voters"`
			}
			if err := app.call("polls.results", api.MessageRef{ID: args[0]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() {
				fmt.Fprintf(app.out, "%s  (%d voters)\n", app.paint("1", res.Question), res.Voters)
				for _, o := range res.Options {
					bar := strings.Repeat("█", o.Votes)
					fmt.Fprintf(app.out, "  %-24s %3d %s %s\n", trunc(o.Name, 24), o.Votes, app.paint("32", bar), app.paint("2", strings.Join(o.Voters, ", ")))
				}
			})
		},
	}
	cmd.AddCommand(create, vote, results)
	return cmd
}

func cmdDownload(app *App) *cobra.Command {
	var chat, out string
	cmd := &cobra.Command{
		Use:   "download <message-id>",
		Short: "Save the photo, video, audio or document from a message",
		Long:  "Downloads and decrypts media. If WhatsApp's copy expired, wassup asks the sender's phone to re-upload it.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res api.DownloadResult
			if err := app.call("messages.download", api.DownloadParams{Chat: chat, ID: args[0], Out: out}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("saved %s (%s)", res.Path, humanSize(res.Size)) })
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	cmd.Flags().StringVarP(&out, "out", "o", "", "output file or folder")
	return cmd
}
