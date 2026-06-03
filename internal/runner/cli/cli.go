// Package cli implements the single-shot "cli" run mode: a cobra command that
// performs one scaling action, prints the result, and exits (non-zero on error).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rahadiangg/siesta/internal/config"
	"github.com/rahadiangg/siesta/internal/scaler"
)

// Version is the build version, overridden via -ldflags at release time.
var Version = "dev"

// newScaler is the provider constructor; overridable in tests.
var newScaler = scaler.New

// usageError marks errors caused by bad invocation (bad flags, missing/invalid
// arguments) so Main can map them to exit code 2.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// Main runs cli mode and returns a process exit code:
// 0 success, 1 runtime/scale error, 2 bad arguments.
func Main(args []string, getenv config.Getenv, stdout, stderr io.Writer) int {
	cmd := newRootCmd(getenv, stdout)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	err := cmd.Execute()
	if err == nil {
		return 0
	}

	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(stderr, "error:", err)
		fmt.Fprintln(stderr, "run 'siesta --help' for usage")
		return 2
	}
	fmt.Fprintln(stderr, "error:", err)
	return 1
}

// commonFlags are shared by the scale (root) and describe commands.
type commonFlags struct {
	mode       string
	provider   string
	region     string
	endpoint   string
	cluster    string
	nodePool   string
	nodePoolID string
	jsonOut    bool
}

func (c commonFlags) settings() config.Settings {
	return config.Settings{
		Provider:     c.provider,
		Region:       c.region,
		Endpoint:     c.endpoint,
		Cluster:      c.cluster,
		NodePoolName: c.nodePool,
		NodePoolID:   c.nodePoolID,
	}
}

// newRootCmd builds the siesta command with flags defaulted from the environment.
func newRootCmd(getenv config.Getenv, stdout io.Writer) *cobra.Command {
	base := config.SettingsFromEnv(getenv)

	var c commonFlags
	var (
		count       int
		dryRun      bool
		scaleGroup  string
		az          string
		flavor      string
		scalePolicy string
	)

	cmd := &cobra.Command{
		Use:     "siesta",
		Version: Version,
		Short:   "Scale a Kubernetes node pool to a desired node count",
		Long: "siesta scales a Kubernetes node pool to a desired node count.\n\n" +
			"The schedule lives in an external cron (Huawei FunctionGraph timer, AWS\n" +
			"EventBridge, ...); siesta just applies the desired state it is told. Each\n" +
			"invocation sets the node pool to --count nodes (0 is allowed). It is\n" +
			"idempotent (no write when already at the target) and returns once the API\n" +
			"accepts the change (nodes provision/drain asynchronously).\n\n" +
			"By default --count is the pool-wide total. To scale a single scale group\n" +
			"(a specific AZ/flavor on Huawei CCE), add --scale-group or --az [--flavor];\n" +
			"use `siesta describe` to list a pool's scale groups.\n\n" +
			"Credentials are read from the environment:\n" +
			"  HUAWEICLOUD_SDK_AK, HUAWEICLOUD_SDK_SK, HUAWEICLOUD_PROJECT_ID\n" +
			"  HUAWEICLOUD_SDK_SECURITY_TOKEN (optional, for temporary credentials)",
		Example: "  # scale the whole pool up\n" +
			"  siesta --cluster <id> --nodepool prod --count 3 --region cn-north-4\n\n" +
			"  # scale only one AZ (e.g. for zonal storage), preview first\n" +
			"  siesta --cluster <id> --nodepool prod --az ap-southeast-4c --count 1 --dry-run\n\n" +
			"  # inspect a pool's scale groups\n" +
			"  siesta describe --cluster <id> --nodepool prod --region cn-north-4",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			if count < 0 {
				return usageError{errors.New("--count is required and must be >= 0")}
			}
			req := scaler.Request{
				Cluster:      c.cluster,
				NodePoolName: c.nodePool,
				NodePoolID:   c.nodePoolID,
				Desired:      int32(count),
				DryRun:       dryRun,
				ScaleGroup:   scaleGroup,
				AZ:           az,
				Flavor:       flavor,
				ScalePolicy:  scalePolicy,
			}
			if err := req.Validate(); err != nil {
				return usageError{err}
			}
			s, err := newScaler(c.provider, config.CredsFromEnv(getenv), c.settings().Options())
			if err != nil {
				return err
			}
			return run(context.Background(), s, req, c.jsonOut, stdout)
		},
	}

	// Shared flags are persistent so the describe subcommand inherits them.
	pf := cmd.PersistentFlags()
	// mode is consumed by the top-level dispatcher; accepted here so it is not an
	// "unknown flag".
	pf.StringVar(&c.mode, "mode", "cli", "run mode (cli|fg)")
	pf.StringVar(&c.provider, "provider", base.Provider, "scaler provider")
	pf.StringVar(&c.region, "region", base.Region, "provider region (e.g. cn-north-4)")
	pf.StringVar(&c.endpoint, "endpoint", base.Endpoint, "provider endpoint override")
	pf.StringVar(&c.cluster, "cluster", base.Cluster, "cluster ID")
	pf.StringVar(&c.nodePool, "nodepool", base.NodePoolName, "node pool name")
	pf.StringVar(&c.nodePoolID, "nodepool-id", base.NodePoolID, "node pool ID (skips name lookup)")
	pf.BoolVar(&c.jsonOut, "json", false, "print result as JSON")

	// Scale-only flags live on the root command.
	f := cmd.Flags()
	f.IntVar(&count, "count", -1, "desired node count (required, >= 0)")
	f.BoolVar(&dryRun, "dry-run", false, "compute the change without applying it")
	f.StringVar(&scaleGroup, "scale-group", "", "target a single scale group by name (see `siesta describe`)")
	f.StringVar(&az, "az", "", "target the scale group bound to this availability zone")
	f.StringVar(&flavor, "flavor", "", "disambiguate --az when multiple groups share a zone")
	f.StringVar(&scalePolicy, "scale-policy", "", "multi-group scale-up policy: random|azbalance")

	cmd.SetFlagErrorFunc(func(_ *cobra.Command, e error) error { return usageError{e} })
	cmd.AddCommand(newDescribeCmd(getenv, &c, stdout))
	return cmd
}

// newDescribeCmd builds the read-only `describe` subcommand.
func newDescribeCmd(getenv config.Getenv, c *commonFlags, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:           "describe",
		Short:         "List a node pool's scale groups (AZ/flavor/counts)",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			req := scaler.Request{Cluster: c.cluster, NodePoolName: c.nodePool, NodePoolID: c.nodePoolID}
			// Reuse Request.Validate but ignore the count rule (describe needs none).
			if req.Cluster == "" || (req.NodePoolName == "" && req.NodePoolID == "") {
				return usageError{errors.New("--cluster and --nodepool (or --nodepool-id) are required")}
			}
			s, err := newScaler(c.provider, config.CredsFromEnv(getenv), c.settings().Options())
			if err != nil {
				return err
			}
			d, ok := s.(scaler.Describer)
			if !ok {
				return fmt.Errorf("provider %q does not support describe", c.provider)
			}
			return describe(context.Background(), d, req, c.jsonOut, stdout)
		},
	}
}

// run performs the scale and prints the result. It is the pure, testable core.
func run(ctx context.Context, s scaler.Scaler, req scaler.Request, jsonOut bool, stdout io.Writer) error {
	res, err := s.Scale(ctx, req)
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	action := "scaled"
	if !res.Changed {
		action = "no change (already at desired)"
	}
	if res.DryRun {
		action = "dry-run: would scale"
	}
	target := res.NodePoolID
	if res.ScaleGroup != "" {
		target = res.NodePoolID + " group=" + res.ScaleGroup
	}
	fmt.Fprintf(stdout, "%s: provider=%s nodePool=%s previous=%d desired=%d\n",
		action, res.Provider, target, res.Previous, res.Desired)
	return nil
}

// describe fetches and prints a node pool's scale groups. Pure, testable core.
func describe(ctx context.Context, d scaler.Describer, req scaler.Request, jsonOut bool, stdout io.Writer) error {
	info, err := d.Describe(ctx, req)
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	fmt.Fprintf(stdout, "node pool %s (%s) — total %d\n", info.Name, info.ID, info.Total)
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "GROUP\tAZ\tFLAVOR\tDESIRED\tEXISTING\tAUTOSCALING")
	for _, g := range info.Groups {
		az := g.AZ
		if az == "" {
			az = "random"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%t\n", g.Name, az, g.Flavor, g.Desired, g.Existing, g.Autoscaling)
	}
	return tw.Flush()
}
