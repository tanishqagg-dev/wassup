package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tanishqagg-dev/wassup/internal/api"
)

func cmdChats(app *App) *cobra.Command {
	var p api.ListChatsParams
	var groups, dms, channels, archived, onlyArchived bool
	cmd := &cobra.Command{
		Use:     "chats [filter]",
		Short:   "List chats, most recent first",
		Example: "  wassup chats\n  wassup chats --unread\n  wassup chats --groups -n 100\n  wassup chats fam",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				p.Query = args[0]
			}
			switch {
			case groups:
				p.Kind = "group"
			case dms:
				p.Kind = "dm"
			case channels:
				p.Kind = "channel"
			}
			if onlyArchived {
				p.Archived = "only"
			} else if archived {
				p.Archived = "include"
			}
			var chats []api.Chat
			if err := app.call("chats.list", p, &chats); err != nil {
				return err
			}
			if chats == nil {
				chats = []api.Chat{}
			}
			return app.emit(chats, func() { app.printChats(chats) })
		},
	}
	f := cmd.Flags()
	f.IntVarP(&p.Limit, "limit", "n", 50, "how many chats")
	f.BoolVarP(&p.Unread, "unread", "u", false, "only chats with unread messages")
	f.BoolVar(&groups, "groups", false, "only groups")
	f.BoolVar(&dms, "dms", false, "only one-to-one chats")
	f.BoolVar(&channels, "channels", false, "only channels")
	f.BoolVar(&archived, "archived", false, "include archived chats")
	f.BoolVar(&onlyArchived, "only-archived", false, "only archived chats")
	return cmd
}

func boolp(b bool) *bool { return &b }

func cmdChat(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "chat", Short: "Archive, pin, mute, mark, delete or inspect a chat"}
	update := func(use, short string, build func(args []string) api.UpdateChatParams, nargs int) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.ExactArgs(nargs),
			RunE: func(c *cobra.Command, args []string) error {
				var res map[string]any
				if err := app.call("chats.update", build(args), &res); err != nil {
					return err
				}
				return app.emit(res, func() { app.ok("%v", res["changed"]) })
			},
		}
	}
	var muteFor string
	mute := update("mute <chat>", "Mute notifications (default 8h; --for 7d or forever)", func(a []string) api.UpdateChatParams {
		d := muteFor
		if d == "" {
			d = "8h"
		}
		return api.UpdateChatParams{Chat: a[0], Mute: d}
	}, 1)
	mute.Flags().StringVar(&muteFor, "for", "", "duration like 8h, 7d, or forever")
	cmd.AddCommand(
		update("archive <chat>", "Archive a chat", func(a []string) api.UpdateChatParams { return api.UpdateChatParams{Chat: a[0], Archive: boolp(true)} }, 1),
		update("unarchive <chat>", "Unarchive a chat", func(a []string) api.UpdateChatParams { return api.UpdateChatParams{Chat: a[0], Archive: boolp(false)} }, 1),
		update("pin <chat>", "Pin a chat to the top", func(a []string) api.UpdateChatParams { return api.UpdateChatParams{Chat: a[0], Pin: boolp(true)} }, 1),
		update("unpin <chat>", "Unpin a chat", func(a []string) api.UpdateChatParams { return api.UpdateChatParams{Chat: a[0], Pin: boolp(false)} }, 1),
		mute,
		update("unmute <chat>", "Unmute a chat", func(a []string) api.UpdateChatParams { return api.UpdateChatParams{Chat: a[0], Mute: "off"} }, 1),
		update("mark-unread <chat>", "Mark a chat as unread", func(a []string) api.UpdateChatParams { return api.UpdateChatParams{Chat: a[0], Read: boolp(false)} }, 1),
		update("mark-read <chat>", "Clear the unread badge (use `wassup read` to also send read receipts)", func(a []string) api.UpdateChatParams { return api.UpdateChatParams{Chat: a[0], Read: boolp(true)} }, 1),
		update("disappearing <chat> <off|24h|7d|90d>", "Set disappearing messages", func(a []string) api.UpdateChatParams {
			return api.UpdateChatParams{Chat: a[0], Disappearing: a[1]}
		}, 2),
		&cobra.Command{
			Use: "delete <chat>", Short: "Delete a chat on all your devices", Args: cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error {
				var res map[string]any
				if err := app.call("chats.delete", api.ChatRef{Chat: args[0]}, &res); err != nil {
					return err
				}
				return app.emit(res, func() { app.ok("deleted %v", res["deleted"]) })
			},
		},
		&cobra.Command{
			Use: "info <chat>", Short: "Show chat details", Args: cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error {
				var res struct {
					Chat     api.Chat `json:"chat"`
					Messages int      `json:"messages_stored"`
				}
				if err := app.call("chats.info", api.ChatRef{Chat: args[0]}, &res); err != nil {
					return err
				}
				return app.emit(res, func() {
					ch := res.Chat
					muted := ""
					if ch.MutedUntil != nil {
						muted = "until " + whenp(ch.MutedUntil)
						if ch.MutedUntil.Year() == 9999 {
							muted = "forever"
						}
					}
					app.kv([2]string{"name", ch.Name}, [2]string{"jid", ch.JID}, [2]string{"kind", ch.Kind},
						[2]string{"unread", fmt.Sprint(ch.Unread)}, [2]string{"last message", whenp(ch.LastMessageAt)},
						[2]string{"archived", yesno(ch.Archived)}, [2]string{"pinned", yesno(ch.Pinned)},
						[2]string{"muted", muted}, [2]string{"disappearing", ch.Disappearing},
						[2]string{"stored messages", fmt.Sprint(res.Messages)})
				})
			},
		},
	)
	return cmd
}

func cmdContacts(app *App) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "contacts [search]",
		Short: "List or search your contacts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := api.ListContactsParams{Limit: limit}
			if len(args) == 1 {
				p.Query = args[0]
			}
			var list []api.Contact
			if err := app.call("contacts.list", p, &list); err != nil {
				return err
			}
			if list == nil {
				list = []api.Contact{}
			}
			return app.emit(list, func() {
				if len(list) == 0 {
					fmt.Fprintln(app.out, app.paint("2", "no contacts found (they sync from your phone shortly after login)"))
					return
				}
				tw := table(app.out)
				fmt.Fprintln(tw, "NAME\tPHONE\tPUSH NAME\tJID")
				for _, c := range list {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", trunc(c.Name, 30), c.Phone, trunc(c.PushName, 24), c.JID)
				}
				tw.Flush()
			})
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 100, "max results")
	return cmd
}

func cmdCheck(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "check <phone...>",
		Short:   "Check whether numbers are on WhatsApp",
		Example: "  wassup check +14155550123 +919876543210",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res []api.CheckResult
			if err := app.call("users.check", api.CheckParams{Phones: args}, &res); err != nil {
				return err
			}
			return app.emit(res, func() {
				tw := table(app.out)
				for _, r := range res {
					state := app.paint("31", "not on WhatsApp")
					if r.OnWhatsApp {
						state = app.paint("32", "on WhatsApp")
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Query, state, r.JID, r.BusinessName)
				}
				tw.Flush()
			})
		},
	}
}

func cmdResolve(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "resolve <name|phone|jid>",
		Short: "Show exactly which chat a name or number points to",
		Long:  "Useful before sending: agents can resolve a name, check the result, then send to the JID.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r api.Resolved
			if err := app.call("resolve", api.ResolveParams{Query: strings.Join(args, " ")}, &r); err != nil {
				return err
			}
			return app.emit(r, func() {
				app.kv([2]string{"name", r.Name}, [2]string{"jid", r.JID}, [2]string{"kind", r.Kind}, [2]string{"phone", r.Phone}, [2]string{"matched by", r.Via})
			})
		},
	}
}

func cmdUser(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Look up a person's profile or picture"}
	info := &cobra.Command{
		Use: "info <person>", Short: "Profile, about text, business details", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var u api.UserInfo
			if err := app.call("users.info", api.UserRef{User: args[0]}, &u); err != nil {
				return err
			}
			return app.emit(u, func() {
				app.kv([2]string{"name", u.Name}, [2]string{"push name", u.PushName}, [2]string{"phone", u.Phone}, [2]string{"jid", u.JID},
					[2]string{"lid", u.LID}, [2]string{"about", u.About}, [2]string{"devices", fmt.Sprint(u.Devices)},
					[2]string{"business", u.BusinessName}, [2]string{"email", u.Email}, [2]string{"website", strings.Join(u.Website, ", ")},
					[2]string{"address", u.Address}, [2]string{"blocked", yesno(u.Blocked)}, [2]string{"picture", u.PictureURL})
			})
		},
	}
	var out string
	var preview bool
	avatar := &cobra.Command{
		Use: "avatar <person|group>", Short: "Download a profile or group picture", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r api.DownloadResult
			if err := app.call("users.avatar", api.AvatarParams{User: args[0], Out: out, Preview: preview}, &r); err != nil {
				return err
			}
			return app.emit(r, func() { app.ok("saved %s", r.Path) })
		},
	}
	avatar.Flags().StringVarP(&out, "out", "o", "", "output file or folder")
	avatar.Flags().BoolVar(&preview, "preview", false, "small thumbnail")
	var presenceCmd = &cobra.Command{
		Use: "watch <person>", Short: "Get online/last-seen events for someone in `wassup events`", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("presence.subscribe", api.UserRef{User: args[0]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("watching %v; %v", res["subscribed"], res["note"]) })
		},
	}
	cmd.AddCommand(info, avatar, presenceCmd)
	return cmd
}

func cmdBlock(app *App, unblock bool) *cobra.Command {
	use, short := "block <person>", "Block someone"
	if unblock {
		use, short = "unblock <person>", "Unblock someone"
	}
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("users.block", api.BlockParams{User: args[0], Unblock: unblock}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("blocked: %v", res["blocked"]) })
		},
	}
}

func cmdBlocklist(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "blocklist", Short: "List blocked people", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var list []api.Contact
			if err := app.call("users.blocklist", nil, &list); err != nil {
				return err
			}
			if list == nil {
				list = []api.Contact{}
			}
			return app.emit(list, func() {
				if len(list) == 0 {
					fmt.Fprintln(app.out, "nobody is blocked")
				}
				for _, c := range list {
					fmt.Fprintf(app.out, "%s  %s\n", c.Name, c.JID)
				}
			})
		},
	}
}

func cmdTyping(app *App) *cobra.Command {
	var recording, stop bool
	cmd := &cobra.Command{
		Use: "typing <chat>", Short: "Show \"typing...\" (or recording) in a chat", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state := "typing"
			if recording {
				state = "recording"
			}
			if stop {
				state = "paused"
			}
			var res map[string]any
			if err := app.call("presence.typing", api.TypingParams{Chat: args[0], State: state}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("%s", state) })
		},
	}
	cmd.Flags().BoolVar(&recording, "recording", false, "show recording audio")
	cmd.Flags().BoolVar(&stop, "stop", false, "clear the indicator")
	return cmd
}

func cmdPresence(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "presence <online|offline>",
		Short: "Appear online or offline",
		Long:  "While wassup appears online, your phone stops showing notifications (same as WhatsApp Web). Default is offline.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			on := strings.EqualFold(args[0], "online") || strings.EqualFold(args[0], "on")
			var res map[string]any
			if err := app.call("presence.set", api.PresenceParams{Online: on}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("presence: %s", args[0]) })
		},
	}
}
