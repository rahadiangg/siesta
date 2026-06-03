// Package scaler defines the cloud-agnostic node-pool scaling contract and a
// small registry so concrete providers (Huawei CCE today, AWS EKS / DigitalOcean
// later) can be selected by name without the runners knowing the details.
package scaler

import (
	"context"
	"fmt"
	"sort"
)

// Scale policy values for distributing nodes across multiple scale groups.
const (
	ScalePolicyRandom    = "random"
	ScalePolicyAZBalance = "azbalance"
)

// Request describes a single desired-state scaling action.
//
// By default Desired is the node pool's total count. The optional scale-group
// selector (ScaleGroup, or AZ[+Flavor]) narrows the action to a single group
// within the pool (a provider-specific capability, e.g. Huawei CCE scale
// groups); when it is set, Desired is the target count for that group.
type Request struct {
	Cluster      string // provider cluster ID
	NodePoolName string // node pool name (preferred lookup) ...
	NodePoolID   string // ... or explicit ID, which skips the name lookup
	Desired      int32  // target node count (0 is valid)
	DryRun       bool   // when true, compute the change but do not apply it

	// Scale-group selector (all optional). ScaleGroup (exact name) wins if set;
	// otherwise AZ (+ Flavor to disambiguate) resolves to a group.
	ScaleGroup  string
	AZ          string
	Flavor      string
	ScalePolicy string // "" | ScalePolicyRandom | ScalePolicyAZBalance
}

// TargetsGroup reports whether the request narrows scaling to a single scale
// group rather than the whole node pool.
func (r Request) TargetsGroup() bool {
	return r.ScaleGroup != "" || r.AZ != "" || r.Flavor != ""
}

// Validate reports whether the request is well-formed.
func (r Request) Validate() error {
	if r.Cluster == "" {
		return fmt.Errorf("cluster is required")
	}
	if r.NodePoolName == "" && r.NodePoolID == "" {
		return fmt.Errorf("one of nodePool name or id is required")
	}
	if r.Desired < 0 {
		return fmt.Errorf("desired count must be >= 0, got %d", r.Desired)
	}
	switch r.ScalePolicy {
	case "", ScalePolicyRandom, ScalePolicyAZBalance:
	default:
		return fmt.Errorf("invalid scale policy %q (want %q or %q)", r.ScalePolicy, ScalePolicyRandom, ScalePolicyAZBalance)
	}
	return nil
}

// Result is the outcome of a scaling action.
type Result struct {
	Provider   string `json:"provider"`
	NodePoolID string `json:"nodePoolId"`
	ScaleGroup string `json:"scaleGroup,omitempty"` // resolved group; empty for pool-total
	Previous   int32  `json:"previous"`
	Desired    int32  `json:"desired"`
	Changed    bool   `json:"changed"` // false when already at desired (idempotent skip)
	DryRun     bool   `json:"dryRun"`
}

// Creds carries the credentials a provider needs. Runners populate it from
// different sources (process env for cli, FunctionGraph agency for fg) so the
// provider does not care where they came from. SecurityToken is empty for
// permanent AK/SK and set for temporary/agency credentials.
type Creds struct {
	AK            string
	SK            string
	ProjectID     string
	SecurityToken string
}

// Options holds non-secret provider configuration.
type Options struct {
	Region   string
	Endpoint string // optional override for regions the SDK enum does not know
}

// Scaler is implemented by every cloud provider.
type Scaler interface {
	Scale(ctx context.Context, r Request) (Result, error)
}

// ScaleGroupInfo describes one scale group within a node pool.
type ScaleGroupInfo struct {
	Name        string `json:"name"`
	AZ          string `json:"az"`
	Flavor      string `json:"flavor"`
	Desired     int32  `json:"desired"`
	Existing    int32  `json:"existing"`
	Autoscaling bool   `json:"autoscaling"`
}

// PoolInfo describes a node pool and its scale groups.
type PoolInfo struct {
	Name   string           `json:"name"`
	ID     string           `json:"id"`
	Total  int32            `json:"total"`
	Groups []ScaleGroupInfo `json:"groups"`
}

// Describer is an optional capability for providers whose node pools have
// inspectable scale groups (e.g. Huawei CCE). Callers type-assert for it.
type Describer interface {
	Describe(ctx context.Context, r Request) (PoolInfo, error)
}

// Constructor builds a Scaler from credentials and options.
type Constructor func(Creds, Options) (Scaler, error)

var registry = map[string]Constructor{}

// Register adds a provider constructor under name. It is meant to be called from
// a provider package's init().
func Register(name string, c Constructor) {
	registry[name] = c
}

// New builds the named provider.
func New(name string, creds Creds, opts Options) (Scaler, error) {
	c, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (available: %v)", name, Providers())
	}
	return c(creds, opts)
}

// Providers returns the sorted list of registered provider names.
func Providers() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
