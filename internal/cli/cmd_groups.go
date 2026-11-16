package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tanishqagg-dev/wassup/internal/api"
)

func (a *App) printGroup(g api.Group) {
	a.kv([2]string{"name", g.Name}, [2]string{"jid", g.JID}, [2]string{"description", g.Description}, [2]string{"owner", g.Owner},
		[2]string{"created", whenp(g.Created)}, [2]string{"members", fmt.Sprint(g.ParticipantCount)},
		[2]string{"only admins send", yesno(g.Announce)}, [2]string{"only admins edit", yesno(g.Locked)},
		[2]string{"approval required", yesno(g.ApprovalRequired)}, [2]string{"who can add", g.MemberAddMode},
		[2]string{"disappearing", g.Disappearing}, [2]string{"community", g.Community}, [2]string{"you are admin", yesno(g.IsAdmin)})
	if len(g.Participants) > 0 {
		fmt.Fprintln(a.out)
		tw := table(a.out)
		for _, p := range g.Participants {
			role := ""
			if p.SuperAdmin {
				role = "owner"
			} else if p.Admin {
				role = "admin"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", trunc(p.Name, 30), p.Phone, role)
		}
		tw.Flush()
	}
}

func cmdGroups(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "groups [filter]", Short: "List groups you're in", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := api.GroupListParams{}
			if len(args) == 1 {
				p.Query = args[0]
			}
			var gs []api.Group
			if err := app.call("groups.list", p, &gs); err != nil {
				return err
			}
			if gs == nil {
				gs = []api.Group{}
			}
			return app.emit(gs, func() {
				tw := table(app.out)
				fmt.Fprintln(tw, "NAME\tMEMBERS\tADMIN\tJID")
				for _, g := range gs {
					fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", trunc(g.Name, 36), g.ParticipantCount, yesno(g.IsAdmin), g.JID)
				}
				tw.Flush()
			})
		},
	}
}

func onOff(s string) (*bool, error) {
	switch strings.ToLower(s) {
	case "":
		return nil, nil
	case "on", "yes", "true", "1":
		return boolp(true), nil
	case "off", "no", "false", "0":
		return boolp(false), nil
	}
	return nil, fmt.Errorf("expected on or off, got %q", s)
}

func cmdGroup(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "group", Short: "Create and manage a group"}
	simple := func(use, short string, n int, run func(args []string) error) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: cobra.MinimumNArgs(n), RunE: func(c *cobra.Command, args []string) error { return run(args) }}
	}
	updated := func(res map[string]any) error {
		return app.emit(res, func() { app.ok("updated %v", res["changed"]) })
	}
	members := func(action string) func(args []string) error {
		return func(args []string) error {
			var res struct {
				Action  string            `json:"action"`
				Results []api.Participant `json:"results"`
				Note    string            `json:"note"`
			}
			if err := app.call("groups.participants", api.GroupParticipantsParams{Group: args[0], Action: action, Participants: args[1:]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() {
				for _, r := range res.Results {
					if r.Error != 0 {
						fmt.Fprintf(app.out, "%s %s (error %d)\n", app.paint("31", "failed"), r.Name, r.Error)
					} else {
						app.ok("%s %s", action, r.Name)
					}
				}
				if res.Note != "" {
					fmt.Fprintln(app.out, res.Note)
				}
			})
		}
	}

	info := simple("info <group>", "Group details and members", 1, func(args []string) error {
		var g api.Group
		if err := app.call("groups.info", api.GroupRef{Group: args[0]}, &g); err != nil {
			return err
		}
		return app.emit(g, func() { app.printGroup(g) })
	})
	var community bool
	var parent, desc string
	create := &cobra.Command{
		Use: "create <name> <person...>", Short: "Create a group", Args: cobra.MinimumNArgs(1),
		Example: `  wassup group create "Trip 2026" "Alice" "+14155550123" "Bob"`,
		RunE: func(c *cobra.Command, args []string) error {
			var res struct {
				Group    api.Group         `json:"group"`
				NotAdded []api.Participant `json:"not_added"`
				Note     string            `json:"note"`
			}
			if err := app.call("groups.create", api.GroupCreateParams{Name: args[0], Participants: args[1:], Community: community, Parent: parent, Description: desc}, &res); err != nil {
				return err
			}
			return app.emit(res, func() {
				app.ok("created %s  %s", res.Group.Name, app.paint("2", res.Group.JID))
				for _, p := range res.NotAdded {
					fmt.Fprintf(app.out, "  couldn't add %s (error %d)\n", p.Name, p.Error)
				}
				if res.Note != "" {
					fmt.Fprintln(app.out, res.Note)
				}
			})
		},
	}
	create.Flags().BoolVar(&community, "community", false, "create a community")
	create.Flags().StringVar(&parent, "community-of", "", "create the group inside this community")
	create.Flags().StringVar(&desc, "description", "", "group description")

	rename := simple("rename <group> <new name...>", "Rename a group", 2, func(args []string) error {
		var res map[string]any
		if err := app.call("groups.update", api.GroupUpdateParams{Group: args[0], Name: strings.Join(args[1:], " ")}, &res); err != nil {
			return err
		}
		return updated(res)
	})
	describe := simple("describe <group> [text...]", "Set the description (no text clears it)", 1, func(args []string) error {
		d := strings.Join(args[1:], " ")
		var res map[string]any
		if err := app.call("groups.update", api.GroupUpdateParams{Group: args[0], Description: &d}, &res); err != nil {
			return err
		}
		return updated(res)
	})
	var announce, locked, approval, memberAdd, disappearing string
	settings := &cobra.Command{
		Use: "settings <group>", Short: "Change who can send, edit and add", Args: cobra.ExactArgs(1),
		Example: "  wassup group settings Team --announce on --member-add admins --disappearing 7d",
		RunE: func(c *cobra.Command, args []string) error {
			p := api.GroupUpdateParams{Group: args[0], MemberAdd: memberAdd, Disappearing: disappearing}
			var err error
			if p.Announce, err = onOff(announce); err != nil {
				return app.fail(api.Errf(api.CodeUsage, "--announce: %v", err))
			}
			if p.Locked, err = onOff(locked); err != nil {
				return app.fail(api.Errf(api.CodeUsage, "--locked: %v", err))
			}
			if p.Approval, err = onOff(approval); err != nil {
				return app.fail(api.Errf(api.CodeUsage, "--approval: %v", err))
			}
			var res map[string]any
			if err := app.call("groups.update", p, &res); err != nil {
				return err
			}
			return updated(res)
		},
	}
	settings.Flags().StringVar(&announce, "announce", "", "on = only admins can send messages")
	settings.Flags().StringVar(&locked, "locked", "", "on = only admins can edit group info")
	settings.Flags().StringVar(&approval, "approval", "", "on = admins approve new members")
	settings.Flags().StringVar(&memberAdd, "member-add", "", "who can add members: admins or all")
	settings.Flags().StringVar(&disappearing, "disappearing", "", "off, 24h, 7d or 90d")
	var removePhoto bool
	photo := &cobra.Command{
		Use: "photo <group> [image]", Short: "Set or remove the group photo", Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			p := api.GroupUpdateParams{Group: args[0], RemovePhoto: removePhoto}
			if len(args) == 2 {
				p.Photo = args[1]
			}
			if p.Photo == "" && !p.RemovePhoto {
				return app.fail(api.Errf(api.CodeUsage, "give an image, or --remove"))
			}
			var res map[string]any
			if err := app.call("groups.update", p, &res); err != nil {
				return err
			}
			return updated(res)
		},
	}
	photo.Flags().BoolVar(&removePhoto, "remove", false, "remove the photo")
	var reset bool
	invite := &cobra.Command{
		Use: "invite <group>", Short: "Print the invite link", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("groups.invite_link", api.GroupInviteParams{Group: args[0], Reset: reset}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { fmt.Fprintln(app.out, res["link"]) })
		},
	}
	invite.Flags().BoolVar(&reset, "reset", false, "revoke the current link and create a new one")
	join := simple("join <invite-link>", "Join a group from an invite link", 1, func(args []string) error {
		var res map[string]any
		if err := app.call("groups.join", api.JoinParams{Link: args[0]}, &res); err != nil {
			return err
		}
		return app.emit(res, func() {
			app.ok("joined %v", res["name"])
			if n, ok := res["note"]; ok {
				fmt.Fprintln(app.out, n)
			}
		})
	})
	preview := simple("preview <invite-link>", "See a group before joining", 1, func(args []string) error {
		var g api.Group
		if err := app.call("groups.preview", api.JoinParams{Link: args[0]}, &g); err != nil {
			return err
		}
		return app.emit(g, func() { app.printGroup(g) })
	})
	requests := simple("requests <group>", "List pending join requests", 1, func(args []string) error {
		var res []map[string]any
		if err := app.call("groups.requests", api.GroupRequestsParams{Group: args[0]}, &res); err != nil {
			return err
		}
		if res == nil {
			res = []map[string]any{}
		}
		return app.emit(res, func() {
			if len(res) == 0 {
				fmt.Fprintln(app.out, "no pending requests")
			}
			for _, r := range res {
				fmt.Fprintf(app.out, "%v  %v\n", r["name"], r["jid"])
			}
		})
	})
	decide := func(action string) func(args []string) error {
		return func(args []string) error {
			var res map[string]any
			if err := app.call("groups.requests", api.GroupRequestsParams{Group: args[0], Action: action, Participants: args[1:]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("%s: %v", action, res["changed"]) })
		}
	}
	leave := simple("leave <group>", "Leave a group", 1, func(args []string) error {
		var res map[string]any
		if err := app.call("groups.leave", api.GroupRef{Group: args[0]}, &res); err != nil {
			return err
		}
		return app.emit(res, func() { app.ok("left %v", res["left"]) })
	})
	cmd.AddCommand(info, create, rename, describe, settings, photo, invite, join, preview, requests,
		simple("add <group> <person...>", "Add members", 2, members("add")),
		simple("remove <group> <person...>", "Remove members", 2, members("remove")),
		simple("promote <group> <person...>", "Make members admins", 2, members("promote")),
		simple("demote <group> <person...>", "Remove admin rights", 2, members("demote")),
		simple("approve <group> [person...]", "Approve join requests (default: all)", 1, decide("approve")),
		simple("reject <group> [person...]", "Reject join requests (default: all)", 1, decide("reject")),
		leave,
	)
	return cmd
}

func cmdCommunity(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "community", Short: "Create communities and link groups to them"}
	create := &cobra.Command{
		Use: "create <name> [person...]", Short: "Create a community", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("groups.create", api.GroupCreateParams{Name: args[0], Participants: args[1:], Community: true}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("community created") })
		},
	}
	link := func(unlink bool) *cobra.Command {
		use, short := "link <community> <group>", "Add a group to a community"
		if unlink {
			use, short = "unlink <community> <group>", "Remove a group from a community"
		}
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.ExactArgs(2),
			RunE: func(c *cobra.Command, args []string) error {
				var res map[string]any
				if err := app.call("communities.link", api.CommunityLinkParams{Community: args[0], Group: args[1], Unlink: unlink}, &res); err != nil {
					return err
				}
				return app.emit(res, func() { app.ok("linked: %v", res["linked"]) })
			},
		}
	}
	groups := &cobra.Command{
		Use: "groups <community>", Short: "List groups in a community", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var res []map[string]any
			if err := app.call("communities.groups", api.GroupRef{Group: args[0]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() {
				for _, g := range res {
					fmt.Fprintf(app.out, "%v  %v\n", g["name"], g["jid"])
				}
			})
		},
	}
	cmd.AddCommand(create, link(false), link(true), groups)
	return cmd
}

func cmdChannels(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "channels", Short: "List channels you follow", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var list []api.Channel
			if err := app.call("channels.list", nil, &list); err != nil {
				return err
			}
			if list == nil {
				list = []api.Channel{}
			}
			return app.emit(list, func() {
				tw := table(app.out)
				fmt.Fprintln(tw, "NAME\tFOLLOWERS\tROLE\tJID")
				for _, c := range list {
					fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", trunc(c.Name, 36), c.Subscribers, c.Role, c.JID)
				}
				tw.Flush()
			})
		},
	}
}

func cmdChannel(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "channel", Short: "Follow, read and react to channels"}
	one := func(use, short, method string, build func(args []string) any, done func(res map[string]any)) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.MinimumNArgs(1),
			RunE: func(c *cobra.Command, args []string) error {
				var res map[string]any
				if err := app.call(method, build(args), &res); err != nil {
					return err
				}
				return app.emit(res, func() { done(res) })
			},
		}
	}
	info := &cobra.Command{
		Use: "info <channel|invite-link>", Short: "Channel details", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var ch api.Channel
			if err := app.call("channels.info", api.ChannelRef{Channel: args[0]}, &ch); err != nil {
				return err
			}
			return app.emit(ch, func() {
				app.kv([2]string{"name", ch.Name}, [2]string{"jid", ch.JID}, [2]string{"followers", fmt.Sprint(ch.Subscribers)},
					[2]string{"verified", yesno(ch.Verified)}, [2]string{"link", ch.Invite}, [2]string{"your role", ch.Role}, [2]string{"description", ch.Description})
			})
		},
	}
	var count int
	posts := &cobra.Command{
		Use: "posts <channel>", Short: "Recent posts", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var list []api.ChannelMessage
			if err := app.call("channels.messages", api.ChannelMessagesParams{Channel: args[0], Count: count}, &list); err != nil {
				return err
			}
			return app.emit(list, func() {
				for _, m := range list {
					var rs []string
					for e, n := range m.Reactions {
						rs = append(rs, fmt.Sprintf("%s%d", e, n))
					}
					fmt.Fprintf(app.out, "%s #%d  %s views  %s\n    %s\n", app.paint("2", m.Time), m.ServerID, fmt.Sprint(m.Views), strings.Join(rs, " "), trunc(m.Text, 200))
				}
			})
		},
	}
	posts.Flags().IntVarP(&count, "count", "n", 20, "how many posts")
	var desc, photo string
	create := &cobra.Command{
		Use: "create <name>", Short: "Create a channel", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var ch api.Channel
			if err := app.call("channels.create", api.ChannelCreateParams{Name: args[0], Description: desc, Photo: photo}, &ch); err != nil {
				return err
			}
			return app.emit(ch, func() { app.ok("created %s  %s", ch.Name, ch.Invite) })
		},
	}
	create.Flags().StringVar(&desc, "description", "", "channel description")
	create.Flags().StringVar(&photo, "photo", "", "channel picture")
	cmd.AddCommand(info, posts, create,
		one("follow <channel|invite-link>", "Follow a channel", "channels.follow", func(a []string) any { return api.ChannelFollowParams{Channel: a[0]} }, func(r map[string]any) { app.ok("following %v", r["name"]) }),
		one("unfollow <channel>", "Unfollow a channel", "channels.follow", func(a []string) any { return api.ChannelFollowParams{Channel: a[0], Unfollow: true} }, func(r map[string]any) { app.ok("unfollowed %v", r["name"]) }),
		one("mute <channel>", "Mute a channel", "channels.mute", func(a []string) any { return api.ChannelMuteParams{Channel: a[0]} }, func(r map[string]any) { app.ok("muted") }),
		one("unmute <channel>", "Unmute a channel", "channels.mute", func(a []string) any { return api.ChannelMuteParams{Channel: a[0], Unmute: true} }, func(r map[string]any) { app.ok("unmuted") }),
		&cobra.Command{
			Use: "react <channel> <post-number> [emoji]", Short: "React to a post (no emoji removes)", Args: cobra.RangeArgs(2, 3),
			RunE: func(c *cobra.Command, args []string) error {
				var sid int
				if _, err := fmt.Sscan(strings.TrimPrefix(args[1], "#"), &sid); err != nil {
					return app.fail(api.Errf(api.CodeUsage, "post number must be the #number from wassup channel posts"))
				}
				emoji := ""
				if len(args) == 3 {
					emoji = args[2]
				}
				var res map[string]any
				if err := app.call("channels.react", api.ChannelReactParams{Channel: args[0], ServerID: sid, Emoji: emoji}, &res); err != nil {
					return err
				}
				return app.emit(res, func() { app.ok("reacted %s", emoji) })
			},
		},
	)
	return cmd
}
