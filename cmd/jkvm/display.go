package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tfyl/jkvm/internal/display"
	"github.com/tfyl/jkvm/internal/kvm"
)

// displaySettle is the pause after setEDID before polling the video state;
// tests shorten it.
var displaySettle = display.SetOptions{}

func (a *app) displayCmd() *cobra.Command {
	statusRun := a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
		return a.withDevice(ctx, false, func(d kvm.Device) error {
			st, err := display.GetStatus(ctx, d)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), st)
		})
	})
	disp := &cobra.Command{
		Use:   "display",
		Short: "Show and change the HDMI video mode and the EDID the device presents",
		Long: `display shows the captured video mode and changes the EDID the JetKVM presents to the
target, which decides the resolutions the target offers. With no subcommand it runs status.`,
		Example: `  jkvm display
  jkvm display presets
  jkvm display set --save prev.edid 'Acer B246WL, 1920x1200'
  jkvm display set "$(cat prev.edid)"
  jkvm display reset`,
		Args: cobra.NoArgs,
		RunE: statusRun,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Print the video mode, the EDID in use and its preset name (JSON)",
		Args:  cobra.NoArgs,
		RunE:  statusRun,
	}
	presets := &cobra.Command{
		Use:   "presets",
		Short: "List the device's EDID presets and their preferred resolutions (JSON)",
		Args:  cobra.NoArgs,
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
			return a.withDevice(ctx, false, func(d kvm.Device) error {
				ps, err := display.Presets(ctx, d)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), ps)
			})
		}),
	}
	var save string
	set := &cobra.Command{
		Use:   "set [flags] PRESET|HEX",
		Short: "Set the EDID, wait for the video to come back and print the new mode (JSON)",
		Long: `set changes the EDID to a preset (by name, case-insensitive) or to EDID hex, then waits
up to --timeout for the device to report video again. The target re-reads the EDID and
may change resolution, and a macOS target may switch from mirroring to extended desktop.
The previous EDID is printed as "previous" and, with --save, written to a file first.`,
		Example: `  jkvm display set --save prev.edid 'Acer B246WL, 1920x1200'
  jkvm display set "$(cat prev.edid)"`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError("display set PRESET|HEX")
			}
			return nil
		},
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			return a.setEDID(ctx, cmd.OutOrStdout(), args[0], save)
		}),
	}
	set.Flags().StringVarP(&save, "save", "s", "", "write the previous EDID hex to this file before changing it")
	var resetSave string
	reset := &cobra.Command{
		Use:   "reset",
		Short: `Set the "` + display.DefaultPreset + `" EDID preset`,
		Args:  cobra.NoArgs,
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
			return a.setEDID(ctx, cmd.OutOrStdout(), display.DefaultPreset, resetSave)
		}),
	}
	reset.Flags().StringVarP(&resetSave, "save", "s", "", "write the previous EDID hex to this file before changing it")
	disp.AddCommand(status, presets, set, reset)
	return disp
}

func (a *app) setEDID(ctx context.Context, w io.Writer, arg, save string) error {
	return a.withDevice(ctx, false, func(d kvm.Device) error {
		ps, err := display.Presets(ctx, d)
		if err != nil {
			return err
		}
		edid, err := display.Resolve(ps, arg)
		if errors.Is(err, display.ErrBadEDID) {
			return usageError(err.Error())
		} else if err != nil {
			return err
		}
		prev, err := display.EDID(ctx, d)
		if err != nil {
			return err
		}
		if save != "" {
			if err := os.WriteFile(save, []byte(prev+"\n"), 0o644); err != nil {
				return err
			}
		}
		v, err := display.Set(ctx, d, edid, displaySettle)
		if err != nil {
			return fmt.Errorf("%w (previous EDID: %s)", err, prev)
		}
		return writeJSON(w, struct {
			display.Video
			Preset   string `json:"preset"`
			EDID     string `json:"edid"`
			Previous string `json:"previous"`
		}{v, presetName(ps, edid), edid, prev})
	})
}

func presetName(ps []display.Preset, edid string) string {
	for _, p := range ps {
		if strings.EqualFold(p.EDID, edid) {
			return p.Name
		}
	}
	return ""
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
