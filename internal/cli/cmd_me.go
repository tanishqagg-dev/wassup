package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tanishqagg-dev/wassup/internal/api"
)

func cmdProfile(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use: "profile", Short: "Show or change your name, about text and photo", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var p api.Profile
			if err := app.call("profile.get", nil, &p); err != nil {
				return err
			}
			return app.emit(p, func() {
				app.kv([2]string{"name", p.Name}, [2]string{"phone", p.Phone}, [2]string{"jid", p.JID}, [2]string{"lid", p.LID},
					[2]string{"about", p.About}, [2]string{"business", p.Business}, [2]string{"picture", p.PictureURL})
			})
		},
	}
	update := func(use, short string, n int, build func(args []string) api.ProfileUpdateParams) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.MinimumNArgs(n),
			RunE: func(c *cobra.Command, args []string) error {
				var res map[string]any
				if err := app.call("profile.update", build(args), &res); err != nil {
					return err
				}
				return app.emit(res, func() { app.ok("updated %v", res["updated"]) })
			},
		}
	}
	var remove bool
	photo := update("photo [image]", "Set (or --remove) your profile photo", 0, func(a []string) api.ProfileUpdateParams {
		p := api.ProfileUpdateParams{RemovePhoto: remove}
		if len(a) > 0 {
			p.Photo = a[0]
		}
		return p
	})
	photo.Flags().BoolVar(&remove, "remove", false, "remove your photo")
	cmd.AddCommand(
		update("name <name...>", "Change the name people see", 1, func(a []string) api.ProfileUpdateParams { return api.ProfileUpdateParams{Name: strings.Join(a, " ")} }),
		update("about <text...>", "Change your about text", 1, func(a []string) api.ProfileUpdateParams {
			s := strings.Join(a, " ")
			return api.ProfileUpdateParams{About: &s}
		}),
		photo,
	)
	return cmd
}

func cmdPrivacy(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use: "privacy", Short: "Show or change privacy settings", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var s map[string]string
			if err := app.call("privacy.get", nil, &s); err != nil {
				return err
			}
			return app.emit(s, func() { app.printPrivacy(s) })
		},
	}
	set := &cobra.Command{
		Use:   "set <setting> <value>",
		Short: "Change one setting",
		Long: `Settings and values:
  last          all, contacts, contact_blacklist, none   (last seen)
  online        all, match_last_seen
  profile       all, contacts, contact_blacklist, none   (photo)
  status        all, contacts, contact_blacklist, none   (about)
  readreceipts  all, none
  groupadd      all, contacts, contact_blacklist, none
  calladd       all, known
  messages      all, contacts
  stickers      contacts, contact_allowlist, none
  defense       on_standard, off`,
		Example: "  wassup privacy set last contacts\n  wassup privacy set readreceipts none",
		Args:    cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			var s map[string]string
			if err := app.call("privacy.set", api.PrivacySetParams{Setting: args[0], Value: args[1]}, &s); err != nil {
				return err
			}
			return app.emit(s, func() { app.printPrivacy(s) })
		},
	}
	cmd.AddCommand(set)
	return cmd
}

func (a *App) printPrivacy(s map[string]string) {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tw := table(a.out)
	for _, k := range keys {
		fmt.Fprintf(tw, "%s\t%s\n", k, s[k])
	}
	tw.Flush()
}

func cmdStory(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "story", Aliases: []string{"status-update"}, Short: "Post and view WhatsApp Status updates"}
	var file, bg string
	var font int
	post := &cobra.Command{
		Use: "post [text...]", Short: "Post a text, photo or video status",
		Example: `  wassup story post "Shipping today" --bg "#1F2A44"
  wassup story post -f sunset.jpg "golden hour"`,
		RunE: func(c *cobra.Command, args []string) error {
			var res api.SendResult
			if err := app.call("status.post", api.StatusPostParams{Text: strings.Join(args, " "), File: file, Background: bg, Font: font}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("posted %s status  %s", res.Type, app.paint("2", res.ID)) })
		},
	}
	post.Flags().StringVarP(&file, "file", "f", "", "image or video")
	post.Flags().StringVar(&bg, "bg", "", "background color for text, #RRGGBB")
	post.Flags().IntVar(&font, "font", 0, "font style 0-9")
	var limit int
	list := &cobra.Command{
		Use: "list", Short: "Status updates from the last 24 hours", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			var msgs []api.Message
			if err := app.call("status.list", api.LimitParams{Limit: limit}, &msgs); err != nil {
				return err
			}
			if msgs == nil {
				msgs = []api.Message{}
			}
			return app.emit(msgs, func() { app.printMessages(msgs, false) })
		},
	}
	list.Flags().IntVarP(&limit, "limit", "n", 50, "how many")
	cmd.AddCommand(post, list)
	return cmd
}

func cmdCalls(app *App) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use: "calls", Short: "Incoming call history (auto-reject is set in wassup config)", Args: cobra.NoArgs,
		Long: `wassup can't place or answer calls (no open WhatsApp library carries call audio),
but it records every incoming call, streams it as a "call" event, and can
auto-reject calls with a text reply. See [calls] in wassup config.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var list []api.Call
			if err := app.call("calls.list", api.LimitParams{Limit: limit}, &list); err != nil {
				return err
			}
			if list == nil {
				list = []api.Call{}
			}
			return app.emit(list, func() {
				if len(list) == 0 {
					fmt.Fprintln(app.out, "no calls recorded yet")
					return
				}
				tw := table(app.out)
				fmt.Fprintln(tw, "TIME\tFROM\tTYPE\tSTATUS\tID")
				for _, c := range list {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Time, c.Name, c.Media, c.Status, c.ID)
				}
				tw.Flush()
			})
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "how many")
	var from string
	reject := &cobra.Command{
		Use: "reject <call-id>", Short: "Reject a ringing call", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("calls.reject", api.CallRejectParams{ID: args[0], From: from}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("rejected") })
		},
	}
	reject.Flags().StringVar(&from, "from", "", "caller, if the call isn't in local history")
	cmd.AddCommand(reject)
	return cmd
}
