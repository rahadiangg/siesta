// Package huaweicce implements the scaler.Scaler interface for Huawei Cloud CCE
// (Cloud Container Engine) node pools. It scales a node pool to a desired node
// count by setting spec.initialNodeCount via the CCE UpdateNodePool API.
//
// Per design, the cluster-autoscaler config on the node pool is left untouched:
// only initialNodeCount (and the name) is sent. If the node pool has autoscaling
// enabled with minNodeCount > 0, the autoscaler may revert a scale-to-0.
package huaweicce

import (
	"context"
	"fmt"
	"strings"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/basic"
	cce "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/model"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/region"

	"github.com/rahadiangg/siesta/internal/scaler"
)

// ProviderName is the registry key for this provider.
const ProviderName = "huawei-cce"

func init() {
	scaler.Register(ProviderName, New)
}

// cceAPI is the narrow slice of the CCE SDK that this package uses. It is
// satisfied by *cce.CceClient in production and by a fake in tests.
type cceAPI interface {
	ListNodePools(*model.ListNodePoolsRequest) (*model.ListNodePoolsResponse, error)
	UpdateNodePool(*model.UpdateNodePoolRequest) (*model.UpdateNodePoolResponse, error)
	ScaleNodePool(*model.ScaleNodePoolRequest) (*model.ScaleNodePoolResponse, error)
}

// defaultGroupName is the scale-group name CCE uses for a node pool's default
// (node-template) group.
const defaultGroupName = "default"

// Scaler scales Huawei CCE node pools.
type Scaler struct {
	api cceAPI
}

// New builds a CCE Scaler from credentials and options. It is registered as the
// "huawei-cce" provider constructor.
func New(c scaler.Creds, o scaler.Options) (scaler.Scaler, error) {
	cred, err := buildCredential(c)
	if err != nil {
		return nil, err
	}

	builder := cce.CceClientBuilder().WithCredential(cred)
	if o.Endpoint != "" {
		builder = builder.WithEndpoint(o.Endpoint)
	} else {
		if o.Region == "" {
			return nil, fmt.Errorf("region is required when no endpoint is set")
		}
		reg, err := region.SafeValueOf(o.Region)
		if err != nil {
			return nil, fmt.Errorf("resolve region %q: %w", o.Region, err)
		}
		builder = builder.WithRegion(reg)
	}

	hc, err := builder.SafeBuild()
	if err != nil {
		return nil, fmt.Errorf("build cce client: %w", err)
	}
	return &Scaler{api: cce.NewCceClient(hc)}, nil
}

// buildCredential constructs basic AK/SK credentials, attaching a security token
// only when present (so it supports both permanent and temporary/agency creds).
func buildCredential(c scaler.Creds) (*basic.Credentials, error) {
	if c.AK == "" || c.SK == "" {
		return nil, fmt.Errorf("access key and secret key are required")
	}
	b := basic.NewCredentialsBuilder().
		WithAk(c.AK).
		WithSk(c.SK).
		WithProjectId(c.ProjectID)
	if c.SecurityToken != "" {
		b = b.WithSecurityToken(c.SecurityToken)
	}
	return b.SafeBuild()
}

// Scale sets the node pool (or a single scale group within it) to the desired
// count. It is idempotent (no API write when already at the target) and returns
// as soon as the API accepts the change.
func (s *Scaler) Scale(ctx context.Context, r scaler.Request) (scaler.Result, error) {
	if err := r.Validate(); err != nil {
		return scaler.Result{}, err
	}
	if r.TargetsGroup() {
		return s.scaleGroup(r)
	}
	return s.scalePool(r)
}

// scalePool sets the whole node pool to r.Desired via UpdateNodePool
// (spec.initialNodeCount). Autoscaling config is never touched.
func (s *Scaler) scalePool(r scaler.Request) (scaler.Result, error) {
	res := scaler.Result{
		Provider:   ProviderName,
		NodePoolID: r.NodePoolID,
		Desired:    r.Desired,
		DryRun:     r.DryRun,
		Previous:   -1, // unknown unless we look it up
	}

	name := r.NodePoolName

	// Resolve the node pool by name (and read its current count) unless an
	// explicit ID was given.
	if r.NodePoolID == "" {
		np, err := s.findNodePool(r.Cluster, r.NodePoolName)
		if err != nil {
			return scaler.Result{}, err
		}
		res.NodePoolID = derefStr(np.Metadata.Uid)
		name = np.Metadata.Name
		res.Previous = currentCount(np)

		// Idempotent skip: already at desired count.
		if res.Previous == r.Desired {
			res.Changed = false
			return res, nil
		}
	}

	if r.DryRun {
		res.Changed = true // intended change; not applied
		return res, nil
	}

	req := &model.UpdateNodePoolRequest{
		ClusterId:  r.Cluster,
		NodepoolId: res.NodePoolID,
		Body: &model.NodePoolUpdate{
			Metadata: &model.NodePoolMetadataUpdate{Name: &name},
			Spec: &model.NodePoolSpecUpdate{
				InitialNodeCount: r.Desired,
				// Autoscaling intentionally left nil — do not touch CA config.
			},
		},
	}
	if _, err := s.api.UpdateNodePool(req); err != nil {
		return scaler.Result{}, fmt.Errorf("update node pool %q: %w", res.NodePoolID, err)
	}

	res.Changed = true
	return res, nil
}

// scaleGroup sets a single scale group to r.Desired via ScaleNodePool. The API
// takes the new pool total, so we compute total + (target - currentGroup).
func (s *Scaler) scaleGroup(r scaler.Request) (scaler.Result, error) {
	np, err := s.resolvePool(r)
	if err != nil {
		return scaler.Result{}, err
	}

	res := scaler.Result{
		Provider:   ProviderName,
		NodePoolID: derefStr(np.Metadata.Uid),
		Desired:    r.Desired,
		DryRun:     r.DryRun,
	}

	groupName, currentG, err := resolveGroup(np, r)
	if err != nil {
		return scaler.Result{}, err
	}
	res.ScaleGroup = groupName
	res.Previous = currentG

	// Idempotent skip: group already at desired count.
	if currentG == r.Desired {
		res.Changed = false
		return res, nil
	}

	total, ok := poolTotal(np)
	if !ok {
		return scaler.Result{}, fmt.Errorf("cannot read current total for node pool %q", res.NodePoolID)
	}
	newTotal := total + (r.Desired - currentG)
	if newTotal < 0 {
		return scaler.Result{}, fmt.Errorf("computed pool total %d < 0 (total=%d, group %q %d->%d)",
			newTotal, total, groupName, currentG, r.Desired)
	}

	if r.DryRun {
		res.Changed = true
		return res, nil
	}

	req := &model.ScaleNodePoolRequest{
		ClusterId:  r.Cluster,
		NodepoolId: res.NodePoolID,
		Body: &model.ScaleNodePoolRequestBody{
			Kind:       "NodePool",
			ApiVersion: "v3",
			Spec: &model.ScaleNodePoolSpec{
				DesiredNodeCount: newTotal,
				ScaleGroups:      []string{groupName},
				Options:          &model.ScaleNodePoolOptions{ScalePolicy: scalePolicyEnum(r.ScalePolicy)},
			},
		},
	}
	if _, err := s.api.ScaleNodePool(req); err != nil {
		return scaler.Result{}, fmt.Errorf("scale node pool %q group %q: %w", res.NodePoolID, groupName, err)
	}

	res.Changed = true
	return res, nil
}

// Describe returns the node pool and its scale groups. Implements scaler.Describer.
func (s *Scaler) Describe(_ context.Context, r scaler.Request) (scaler.PoolInfo, error) {
	np, err := s.resolvePool(r)
	if err != nil {
		return scaler.PoolInfo{}, err
	}
	total, _ := poolTotal(np)
	info := scaler.PoolInfo{
		Name:  np.Metadata.Name,
		ID:    derefStr(np.Metadata.Uid),
		Total: total,
	}
	for _, g := range collectGroups(np) {
		info.Groups = append(info.Groups, scaler.ScaleGroupInfo{
			Name:        g.name,
			AZ:          g.az,
			Flavor:      g.flavor,
			Desired:     groupDesired(np, g.name),
			Existing:    groupExisting(np, g.name),
			Autoscaling: g.autoscaling,
		})
	}
	return info, nil
}

// groupRef is a flattened view of a scale group (default + extension groups).
type groupRef struct {
	name        string
	az          string
	flavor      string
	autoscaling bool
}

// collectGroups flattens a node pool's default group plus extension scale groups.
func collectGroups(np *model.NodePoolResp) []groupRef {
	groups := []groupRef{defaultGroup(np)}
	if np.Spec != nil && np.Spec.ExtensionScaleGroups != nil {
		for _, g := range *np.Spec.ExtensionScaleGroups {
			ref := groupRef{}
			if g.Metadata != nil {
				ref.name = derefStr(g.Metadata.Name)
			}
			if g.Spec != nil {
				ref.az = derefStr(g.Spec.Az)
				ref.flavor = derefStr(g.Spec.Flavor)
				ref.autoscaling = g.Spec.Autoscaling != nil && derefBool(g.Spec.Autoscaling.Enable)
			}
			groups = append(groups, ref)
		}
	}
	return groups
}

// defaultGroup builds the groupRef for the node pool's default (node-template)
// group. Its AZ/flavor come from the node template.
func defaultGroup(np *model.NodePoolResp) groupRef {
	ref := groupRef{name: defaultGroupName}
	if np.Spec != nil {
		if np.Spec.NodeTemplate != nil {
			ref.az = derefStr(np.Spec.NodeTemplate.Az)
			ref.flavor = derefStr(np.Spec.NodeTemplate.Flavor)
		}
		ref.autoscaling = np.Spec.Autoscaling != nil && derefBool(np.Spec.Autoscaling.Enable)
	}
	return ref
}

// resolveGroup picks the target scale group and its current node count. Match by
// ScaleGroup name if given, else by AZ (+ Flavor to disambiguate).
func resolveGroup(np *model.NodePoolResp, r scaler.Request) (name string, current int32, err error) {
	groups := collectGroups(np)

	if r.ScaleGroup != "" {
		for _, g := range groups {
			if g.name == r.ScaleGroup {
				return g.name, groupDesired(np, g.name), nil
			}
		}
		return "", 0, fmt.Errorf("scale group %q not found (available: %s)", r.ScaleGroup, describeGroups(groups))
	}

	// Match by AZ (+ Flavor).
	var matches []groupRef
	for _, g := range groups {
		if r.AZ != "" && g.az != r.AZ {
			continue
		}
		if r.Flavor != "" && g.flavor != r.Flavor {
			continue
		}
		matches = append(matches, g)
	}
	switch len(matches) {
	case 1:
		return matches[0].name, groupDesired(np, matches[0].name), nil
	case 0:
		// Clearer hint when a multi-AZ group exists (e.g. default az=random) that
		// cannot be targeted by a single AZ.
		if r.AZ != "" && hasMultiAZGroup(groups) {
			return "", 0, fmt.Errorf("no scale group is bound to az %q; a multi-AZ group exists but cannot be targeted by AZ (available: %s)", r.AZ, describeGroups(groups))
		}
		return "", 0, fmt.Errorf("no scale group matches az=%q flavor=%q (available: %s)", r.AZ, r.Flavor, describeGroups(groups))
	default:
		return "", 0, fmt.Errorf("multiple scale groups match az=%q; add a flavor to disambiguate (matched: %s)", r.AZ, describeGroups(matches))
	}
}

// hasMultiAZGroup reports whether any group is not bound to a single AZ.
func hasMultiAZGroup(groups []groupRef) bool {
	for _, g := range groups {
		if g.az == "" || g.az == "random" {
			return true
		}
	}
	return false
}

func describeGroups(groups []groupRef) string {
	parts := make([]string, 0, len(groups))
	for _, g := range groups {
		az := g.az
		if az == "" {
			az = "random"
		}
		parts = append(parts, fmt.Sprintf("%s(az=%s,flavor=%s)", g.name, az, g.flavor))
	}
	return strings.Join(parts, ", ")
}

// scalePolicyEnum maps siesta's policy string to the SDK enum (default Random).
func scalePolicyEnum(policy string) *model.ScaleNodePoolOptionsScalePolicy {
	e := model.GetScaleNodePoolOptionsScalePolicyEnum()
	if policy == scaler.ScalePolicyAZBalance {
		return &e.AZ_BALANCE
	}
	return &e.RANDOM
}

// findNodePool returns the single node pool in cluster matching name.
func (s *Scaler) findNodePool(cluster, name string) (*model.NodePoolResp, error) {
	resp, err := s.api.ListNodePools(&model.ListNodePoolsRequest{ClusterId: cluster})
	if err != nil {
		return nil, fmt.Errorf("list node pools: %w", err)
	}
	if resp.Items == nil {
		return nil, fmt.Errorf("node pool %q not found in cluster %q", name, cluster)
	}

	var match *model.NodePoolResp
	for i := range *resp.Items {
		np := &(*resp.Items)[i]
		if np.Metadata != nil && np.Metadata.Name == name {
			if match != nil {
				return nil, fmt.Errorf("multiple node pools named %q in cluster %q", name, cluster)
			}
			match = np
		}
	}
	if match == nil {
		return nil, fmt.Errorf("node pool %q not found in cluster %q", name, cluster)
	}
	return match, nil
}

// resolvePool fetches the full node pool by ID (if given) or name. Unlike the
// fast path in scalePool, group/describe operations always need the full pool
// object, so this matches by Uid when an ID is supplied.
func (s *Scaler) resolvePool(r scaler.Request) (*model.NodePoolResp, error) {
	if r.NodePoolID == "" {
		return s.findNodePool(r.Cluster, r.NodePoolName)
	}
	resp, err := s.api.ListNodePools(&model.ListNodePoolsRequest{ClusterId: r.Cluster})
	if err != nil {
		return nil, fmt.Errorf("list node pools: %w", err)
	}
	if resp.Items != nil {
		for i := range *resp.Items {
			np := &(*resp.Items)[i]
			if np.Metadata != nil && derefStr(np.Metadata.Uid) == r.NodePoolID {
				return np, nil
			}
		}
	}
	return nil, fmt.Errorf("node pool id %q not found in cluster %q", r.NodePoolID, r.Cluster)
}

// currentCount reports the node pool's current node count, preferring the live
// status and falling back to the configured spec.
func currentCount(np *model.NodePoolResp) int32 {
	if np.Status != nil && np.Status.CurrentNode != nil {
		return *np.Status.CurrentNode
	}
	if np.Spec != nil && np.Spec.InitialNodeCount != nil {
		return *np.Spec.InitialNodeCount
	}
	return -1
}

// poolTotal returns the node pool's current total from live status.
func poolTotal(np *model.NodePoolResp) (int32, bool) {
	if np.Status != nil && np.Status.CurrentNode != nil {
		return *np.Status.CurrentNode, true
	}
	return 0, false
}

// groupDesired returns the desired node count for a scale group from status,
// falling back to 0 when the group has no status entry (e.g. zero nodes).
func groupDesired(np *model.NodePoolResp, name string) int32 {
	if st := groupStatus(np, name); st != nil && st.DesiredNodeCount != nil {
		return *st.DesiredNodeCount
	}
	return 0
}

// groupExisting returns the existing node count for a scale group from status.
func groupExisting(np *model.NodePoolResp, name string) int32 {
	if st := groupStatus(np, name); st != nil && st.ExistingNodeCount != nil && st.ExistingNodeCount.Total != nil {
		return *st.ExistingNodeCount.Total
	}
	return 0
}

func groupStatus(np *model.NodePoolResp, name string) *model.ScaleGroupStatus {
	if np.Status == nil || np.Status.ScaleGroupStatuses == nil {
		return nil
	}
	for i := range *np.Status.ScaleGroupStatuses {
		st := &(*np.Status.ScaleGroupStatuses)[i]
		if derefStr(st.Name) == name {
			return st
		}
	}
	return nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefBool(b *bool) bool {
	return b != nil && *b
}
