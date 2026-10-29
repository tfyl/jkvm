// Command jkvm drives a JetKVM over the same protocol as its web UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tfyl/jkvm/internal/kvm"
	"github.com/tfyl/jkvm/internal/session"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = ""

func main() { os.Exit(jkvm(os.Args[1:], os.Stdout, os.Stderr)) }

// device is a connected kvm.Device that must be closed.
type device interface {
	kvm.Device
	Close() error
}

// app holds the global flags and the I/O of one jkvm invocation.
type app struct {
	host     string
	password string
	timeout  time.Duration
	verbose  bool

	stdout, stderr io.Writer
	// dial connects to the device; tests replace it with a fake.
	dial func(ctx context.Context, a *app, withVideo bool) (device, error)
	// ran is set once a command's own code starts, so errors before it
	// (flags, arguments, unknown commands) are usage errors.
	ran bool
}

// usageError is a mistake in the command line: exit code 2.
type usageError string

func (e usageError) Error() string { return string(e) }

// errShownUsage is a usage error whose message has already been printed.
var errShownUsage = errors.New("usage shown")

// jkvm runs the command line and returns the exit code: 0 ok, 1 failure,
// 2 usage error.
func jkvm(args []string, stdout, stderr io.Writer) int {
	a := &app{stdout: stdout, stderr: stderr, dial: dialSession}
	return a.execute(args)
}

func (a *app) execute(args []string) int {
	root := a.rootCmd()
	root.SetArgs(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	if !errors.Is(err, errShownUsage) {
		fmt.Fprintln(a.stderr, "jkvm:", err)
	}
	_, usage := errors.AsType[usageError](err)
	if usage || !a.ran || errors.Is(err, errShownUsage) {
		return 2
	}
	return 1
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "jkvm",
		Short: "Drive a JetKVM from the command line",
		Long: `jkvm drives a JetKVM from the command line: screenshots, typing, key combos,
mouse, virtual media and raw JSON-RPC, with no browser.

The device is set with --host or JKVM_HOST, e.g. JKVM_HOST=http://jetkvm.local.
Results are printed on stdout, mostly as JSON. Exit codes: 0 ok, 1 failure,
2 usage error.`,
		Example: `  export JKVM_HOST=http://jetkvm.local
  jkvm info
  jkvm snapshot -o screen.png
  jkvm key win+r
  jkvm type -e 'notepad\n'`,
		Version:       buildVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
		// A bare "jkvm" prints the usage and exits 2, as a usage error.
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SetOut(a.stderr)
			_ = cmd.Usage()
			return errShownUsage
		},
	}
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.SetVersionTemplate("jkvm {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err.Error()) })

	pf := root.PersistentFlags()
	pf.StringVar(&a.host, "host", os.Getenv("JKVM_HOST"), "device URL, e.g. http://jetkvm.local (env JKVM_HOST; required)")
	pf.StringVar(&a.password, "password", os.Getenv("JKVM_PASSWORD"), "local password; empty when the device has none (env JKVM_PASSWORD)")
	pf.DurationVar(&a.timeout, "timeout", 60*time.Second, "limit for the whole command")
	pf.BoolVarP(&a.verbose, "verbose", "v", false, "log signalling, ICE and connection state to stderr")

	root.AddCommand(
		a.infoCmd(),
		a.snapshotCmd(false),
		a.snapshotCmd(true),
		a.typeCmd(),
		a.keyCmd(),
		a.mouseCmd(),
		a.mediaCmd(),
		a.rpcCmd(),
		a.eventsCmd(),
		a.displayCmd(),
		a.versionCmd(),
	)
	return root
}

// run wraps a command body: it marks the command as started and applies
// --timeout to its context.
func (a *app) run(f func(ctx context.Context, cmd *cobra.Command, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		a.ran = true
		ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout)
		defer cancel()
		return f(ctx, cmd, args)
	}
}

// numericArgs makes a command accept negative numbers as arguments
// ("mouse scroll -3"), which pflag would read as shorthand flags. The command
// must set DisableFlagParsing; this parses the flags itself, with each
// negative number swapped for a placeholder so arguments keep their order.
func numericArgs(next func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		const placeholder = "\x00neg"
		var negatives []string
		flagArgs := make([]string, len(args))
		for i, s := range args {
			if isNegativeNumber(s) {
				flagArgs[i] = placeholder + strconv.Itoa(len(negatives))
				negatives = append(negatives, s)
			} else {
				flagArgs[i] = s
			}
		}
		// ParseFlags does nothing while DisableFlagParsing is set.
		cmd.DisableFlagParsing = false
		err := cmd.ParseFlags(flagArgs)
		cmd.DisableFlagParsing = true
		if err != nil {
			if errors.Is(err, pflag.ErrHelp) {
				return cmd.Help()
			}
			return usageError(err.Error())
		}
		if help, _ := cmd.Flags().GetBool("help"); help {
			return cmd.Help()
		}
		rest := cmd.Flags().Args()
		for i, s := range rest {
			if n, ok := strings.CutPrefix(s, placeholder); ok {
				j, _ := strconv.Atoi(n)
				rest[i] = negatives[j]
			}
		}
		return next(cmd, rest)
	}
}

func isNegativeNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil && strings.HasPrefix(s, "-")
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the jkvm version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "jkvm", buildVersion())
			return err
		},
	}
}

// normalizeHost adds http:// when the scheme is missing and drops trailing
// slashes.
func normalizeHost(h string) string {
	if !strings.Contains(h, "://") {
		h = "http://" + h
	}
	return strings.TrimRight(h, "/")
}

// buildVersion is the -ldflags version, else the module version recorded by
// go install, else "dev".
func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func dialSession(ctx context.Context, a *app, withVideo bool) (device, error) {
	return session.Dial(ctx, session.Options{
		BaseURL:  normalizeHost(a.host),
		Password: a.password,
		Video:    withVideo,
		Verbose:  a.verbose,
	})
}

// withDevice dials, runs f, and always closes the session.
func (a *app) withDevice(ctx context.Context, withVideo bool, f func(kvm.Device) error) error {
	if a.host == "" {
		return usageError("no device given; set --host or JKVM_HOST (e.g. http://jetkvm.local)")
	}
	d, err := a.dial(ctx, a, withVideo)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return f(d)
}
