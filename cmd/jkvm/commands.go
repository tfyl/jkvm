package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tfyl/jkvm/internal/hid"
	"github.com/tfyl/jkvm/internal/kvm"
	"github.com/tfyl/jkvm/internal/media"
	"github.com/tfyl/jkvm/internal/video"
)

func (a *app) infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Print device id, firmware, video, USB/HID state and mounted media (JSON)",
		Args:  cobra.NoArgs,
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
			return a.withDevice(ctx, false, func(d kvm.Device) error {
				out, err := media.Info(ctx, d)
				if err != nil {
					return err
				}
				return printJSON(cmd.OutOrStdout(), out)
			})
		}),
	}
}

func (a *app) snapshotCmd(wait bool) *cobra.Command {
	var (
		out       string
		interval  time.Duration
		threshold float64
		limit     time.Duration
	)
	cmd := &cobra.Command{
		Use:     "snapshot -o FILE.png",
		Short:   "Save the current screen as a PNG",
		Long:    "snapshot requests a keyframe, decodes it with ffmpeg and writes a PNG at the native resolution.\nIt prints \"FILE WIDTHxHEIGHT\". It fails fast when there is no HDMI signal.",
		Example: "  jkvm snapshot -o screen.png",
		Args:    cobra.NoArgs,
	}
	if wait {
		cmd.Use = "wait-screen -o FILE.png [-t 60s]"
		cmd.Short = "Wait until the screen changes, then save it as a PNG"
		cmd.Long = "wait-screen polls frames until more than --threshold of the screen changes, then saves that\nframe like snapshot. It exits 1 when --wait runs out. Raise --timeout too for waits over 60s."
		cmd.Example = "  jkvm key enter && jkvm wait-screen -o next.png -t 30s\n  jkvm --timeout 6m wait-screen -o boot.png -t 5m"
	}
	f := cmd.Flags()
	f.StringVarP(&out, "output", "o", "", "output PNG path (required)")
	if wait {
		f.DurationVarP(&limit, "wait", "t", 60*time.Second, "give up after")
		f.DurationVar(&interval, "interval", time.Second, "poll interval")
		f.Float64Var(&threshold, "threshold", 0.01, "changed-pixel fraction that counts as a change")
	}
	cmd.RunE = a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
		if out == "" {
			return usageError("-o/--output FILE.png is required")
		}
		w := cmd.OutOrStdout()
		err := a.grabFrame(ctx, w, out, wait, limit, interval, threshold)
		if errors.Is(err, video.ErrNoKeyframe) {
			fmt.Fprintln(a.stderr, "jkvm: no keyframe, reconnecting once")
			err = a.grabFrame(ctx, w, out, wait, limit, interval, threshold)
		}
		return err
	})
	return cmd
}

func (a *app) grabFrame(ctx context.Context, w io.Writer, out string, wait bool, limit, interval time.Duration, threshold float64) error {
	return a.withDevice(ctx, true, func(d kvm.Device) error {
		var (
			f   *video.Frame
			err error
		)
		if wait {
			wctx, cancel := context.WithTimeout(ctx, limit)
			defer cancel()
			f, err = video.WaitScreen(wctx, d, video.WaitOptions{Interval: interval, Threshold: threshold})
		} else {
			f, err = video.Snapshot(ctx, d)
		}
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, f.PNG, 0o644); err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s %dx%d\n", out, f.Width, f.Height)
		return err
	})
}

func (a *app) typeCmd() *cobra.Command {
	var (
		layout  string
		delay   int
		escapes bool
	)
	cmd := &cobra.Command{
		Use:   "type [flags] TEXT",
		Short: "Type text through the device-side keyboard macro",
		Long: `type types its argument exactly; a real newline presses Enter and a tab presses Tab.
Every character is checked against the layout before anything is sent; a character
the layout cannot type is a usage error (exit 2).

With -e, \n (Enter), \t (Tab) and \\ (backslash) are expanded first. Other
backslashes stay as they are, so C:\Users types as written.`,
		Example: `  jkvm type -e 'notepad\n'
  jkvm type --layout en-UK 'Get-Partition | Format-Table'
  jkvm type -- '-text that starts with a dash'`,
		RunE: a.run(func(ctx context.Context, _ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError("type takes exactly one text argument")
			}
			text := args[0]
			if escapes {
				text = expandEscapes(text)
			}
			return a.withDevice(ctx, false, func(d kvm.Device) error {
				err := hid.Type(ctx, d, text, hid.TypeOptions{Layout: layout, DelayMS: delay})
				if ue, ok := errors.AsType[*hid.UntypableError](err); ok {
					return usageError(ue.Error())
				}
				return err
			})
		}),
	}
	f := cmd.Flags()
	f.StringVarP(&layout, "layout", "l", os.Getenv("JKVM_LAYOUT"), "en-US or en-UK (default: device setting; env JKVM_LAYOUT)")
	f.IntVarP(&delay, "delay", "d", 0, "ms between characters (default 20)")
	f.BoolVarP(&escapes, "escapes", "e", false, `expand \n (Enter), \t (Tab) and \\ in the text`)
	return cmd
}

// expandEscapes turns \n, \t and \\ into newline, tab and backslash. Other
// backslashes stay literal, so Windows paths survive.
func expandEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func (a *app) keyCmd() *cobra.Command {
	var (
		layout string
		hold   int
	)
	cmd := &cobra.Command{
		Use:   "key [flags] COMBO...",
		Short: "Press key combinations in turn",
		Long: `key presses each combo in turn: modifiers first, then the keys, then all are released.
A combo is keys joined with +, e.g. win+x, ctrl+alt+Delete, enter, f11, ctrl+z.`,
		Example: `  jkvm key ctrl+alt+Delete
  jkvm key win+r
  jkvm key --hold 3000 f12`,
		RunE: a.run(func(ctx context.Context, _ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usageError("key needs at least one combo")
			}
			return a.withDevice(ctx, false, func(d kvm.Device) error {
				return hid.Keys(ctx, d, args, layout, hold)
			})
		}),
	}
	f := cmd.Flags()
	f.StringVarP(&layout, "layout", "l", os.Getenv("JKVM_LAYOUT"), "layout for character keys in combos (env JKVM_LAYOUT)")
	f.IntVar(&hold, "hold", 0, "ms to hold each combo")
	return cmd
}

func parseNumbers(args []string) ([]float64, error) {
	nums := make([]float64, len(args))
	for i, s := range args {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, usageError("bad number " + strconv.Quote(s))
		}
		nums[i] = v
	}
	return nums, nil
}

func (a *app) mouseCmd() *cobra.Command {
	mouse := &cobra.Command{
		Use:   "mouse move|click|scroll",
		Short: "Move, click and scroll the mouse",
		Long:  "mouse drives the absolute pointer. Coordinates are screen pixels, or 0..1 fractions with --norm.",
		RunE: a.run(func(_ context.Context, _ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usageError("mouse move|click|scroll")
			}
			return usageError("unknown mouse subcommand " + strconv.Quote(args[0]))
		}),
	}
	var (
		norm   bool
		button string
		count  int
	)
	point := func(sub string) *cobra.Command {
		cmd := &cobra.Command{
			Use:                sub + " [flags] X Y",
			DisableFlagParsing: true,
		}
		cmd.RunE = numericArgs(a.run(func(ctx context.Context, _ *cobra.Command, args []string) error {
			nums, err := parseNumbers(args)
			if err != nil {
				return err
			}
			if len(nums) != 2 {
				return usageError("mouse " + sub + " X Y")
			}
			var b hid.Button
			switch button {
			case "left":
				b = hid.ButtonLeft
			case "right":
				b = hid.ButtonRight
			case "middle":
				b = hid.ButtonMiddle
			default:
				return usageError("bad --button " + button)
			}
			return a.withDevice(ctx, false, func(d kvm.Device) error {
				p := hid.Point{X: nums[0], Y: nums[1], Normalized: norm}
				if !norm {
					w, h, err := screenSize(ctx, d)
					if err != nil {
						return err
					}
					p.Width, p.Height = w, h
				}
				if sub == "move" {
					return hid.MouseMove(ctx, d, p)
				}
				return hid.MouseClick(ctx, d, p, b, count)
			})
		}))
		cmd.Flags().BoolVarP(&norm, "norm", "n", false, "coordinates are 0..1 fractions of the screen")
		return cmd
	}
	move := point("move")
	move.Short = "Move the pointer to X Y"
	move.Example = "  jkvm mouse move 960 540\n  jkvm mouse move --norm 0.5 0.5"
	click := point("click")
	click.Short = "Move to X Y and click"
	click.Example = "  jkvm mouse click 960 540\n  jkvm mouse click --norm --button right 0.5 0.5\n  jkvm mouse click -c 2 100 200"
	click.Flags().StringVarP(&button, "button", "b", "left", "left|right|middle")
	click.Flags().IntVarP(&count, "count", "c", 1, "click count")

	scroll := &cobra.Command{
		Use:                "scroll DY",
		Short:              "Turn the wheel DY detents; positive scrolls up, negative down",
		Example:            "  jkvm mouse scroll 3\n  jkvm mouse scroll -3",
		DisableFlagParsing: true,
	}
	scroll.RunE = numericArgs(a.run(func(ctx context.Context, _ *cobra.Command, args []string) error {
		nums, err := parseNumbers(args)
		if err != nil {
			return err
		}
		if len(nums) != 1 {
			return usageError("mouse scroll DY")
		}
		return a.withDevice(ctx, false, func(d kvm.Device) error {
			return hid.MouseScroll(ctx, d, int(nums[0]), 0)
		})
	}))
	mouse.AddCommand(move, click, scroll)
	return mouse
}

// screenSize reads the captured resolution from getVideoState.
func screenSize(ctx context.Context, d kvm.Device) (int, int, error) {
	raw, err := d.Call(ctx, "getVideoState", nil)
	if err != nil {
		return 0, 0, fmt.Errorf("getVideoState: %w", err)
	}
	var st struct {
		Ready  bool `json:"ready"`
		Width  int  `json:"width"`
		Height int  `json:"height"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return 0, 0, fmt.Errorf("getVideoState: %w", err)
	}
	if st.Width == 0 || st.Height == 0 {
		return 0, 0, errors.New("no video signal, so pixel coordinates are unknown; use --norm")
	}
	return st.Width, st.Height, nil
}

func (a *app) mediaCmd() *cobra.Command {
	m := &cobra.Command{
		Use:   "media status|mount-url|mount-file|unmount",
		Short: "Mount, inspect and eject virtual media",
		RunE: a.run(func(_ context.Context, _ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usageError("media status|mount-url|mount-file|unmount")
			}
			return usageError("unknown media subcommand " + strconv.Quote(args[0]))
		}),
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Print the mounted image, storage space, stored files and mode (JSON)",
		Args:  cobra.NoArgs,
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
			return a.withDevice(ctx, false, func(d kvm.Device) error {
				out, err := media.Status(ctx, d)
				if err != nil {
					return err
				}
				return printJSON(cmd.OutOrStdout(), out)
			})
		}),
	}
	mount := func(use, short, example string, f func(ctx context.Context, d kvm.Device, arg string, m media.Mode) error) *cobra.Command {
		var mode string
		cmd := &cobra.Command{
			Use:     use,
			Short:   short,
			Example: example,
			RunE: a.run(func(ctx context.Context, _ *cobra.Command, args []string) error {
				md := media.Mode(mode)
				if md != media.ModeCDROM && md != media.ModeDisk {
					return usageError("bad --mode " + mode)
				}
				if len(args) != 1 {
					return usageError("media " + use)
				}
				return a.withDevice(ctx, false, func(d kvm.Device) error { return f(ctx, d, args[0], md) })
			}),
		}
		cmd.Flags().StringVarP(&mode, "mode", "m", "CDROM", "CDROM or Disk")
		return cmd
	}
	mountURL := mount("mount-url [flags] URL",
		"Have the device stream an image from an HTTP(S) server with range requests",
		"  jkvm media mount-url https://example.com/debian.iso",
		media.MountURL)
	mountFile := mount("mount-file [flags] PATH",
		"Upload an image to device storage (resumable) and mount it",
		"  jkvm media mount-file --mode Disk ./memtest.img",
		func(ctx context.Context, d kvm.Device, path string, m media.Mode) error {
			return media.MountFile(ctx, d, path, m, func(sent, total int64) {
				fmt.Fprintf(a.stderr, "\rupload %d/%d MiB", sent>>20, total>>20)
			})
		})
	unmount := &cobra.Command{
		Use:   "unmount",
		Short: "Eject the mounted image",
		Args:  cobra.NoArgs,
		RunE: a.run(func(ctx context.Context, _ *cobra.Command, _ []string) error {
			return a.withDevice(ctx, false, func(d kvm.Device) error { return media.Unmount(ctx, d) })
		}),
	}
	m.AddCommand(status, mountURL, mountFile, unmount)
	return m
}

func (a *app) rpcCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rpc METHOD [JSON-PARAMS]",
		Short: "Call a JSON-RPC method and print the result",
		Long:  "rpc calls any device JSON-RPC method. Some change the device (network, updates, factory reset);\ncheck PROTOCOL.md before calling one you don't know.",
		Example: `  jkvm rpc ping
  jkvm rpc getATXState
  jkvm rpc setATXPowerAction '{"action":"power-short"}'`,
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			if len(args) < 1 || len(args) > 2 {
				return usageError("rpc METHOD [JSON-PARAMS]")
			}
			var params map[string]any
			if len(args) == 2 {
				if err := json.Unmarshal([]byte(args[1]), &params); err != nil {
					return usageError("params must be a JSON object: " + err.Error())
				}
			}
			return a.withDevice(ctx, false, func(d kvm.Device) error {
				out, err := d.Call(ctx, args[0], params)
				if err != nil {
					return err
				}
				return printJSON(cmd.OutOrStdout(), out)
			})
		}),
	}
}

// eventsCmd holds a session open and prints the device's JSON-RPC
// notifications (videoInputState, usbState, ...) as JSON lines.
func (a *app) eventsCmd() *cobra.Command {
	var (
		limit     time.Duration
		withVideo bool
	)
	cmd := &cobra.Command{
		Use:     "events [-t 10s] [--video]",
		Short:   "Print device notifications as JSON lines",
		Example: "  jkvm events -t 30s --video",
		Args:    cobra.NoArgs,
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			return a.withDevice(ctx, withVideo, func(d kvm.Device) error {
				done := time.After(limit)
				for {
					select {
					case ev := <-d.Events():
						line, _ := json.Marshal(map[string]any{"time": time.Now().Format(time.RFC3339Nano), "method": ev.Method, "params": ev.Params})
						fmt.Fprintln(w, string(line))
					case <-done:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			})
		}),
	}
	cmd.Flags().DurationVarP(&limit, "duration", "t", 10*time.Second, "how long to listen")
	cmd.Flags().BoolVar(&withVideo, "video", false, "also receive video (starts the device's encoder)")
	return cmd
}

func printJSON(w io.Writer, raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		_, err = w.Write(append(raw, '\n'))
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
