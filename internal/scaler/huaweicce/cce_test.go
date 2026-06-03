package huaweicce

import (
	"context"
	"errors"
	"testing"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/model"

	"github.com/rahadiangg/siesta/internal/scaler"
)

// fakeAPI records calls and returns canned responses, implementing cceAPI
// without any network or real SDK client.
type fakeAPI struct {
	listResp *model.ListNodePoolsResponse
	listErr  error
	listReqs []*model.ListNodePoolsRequest

	updResp *model.UpdateNodePoolResponse
	updErr  error
	updReqs []*model.UpdateNodePoolRequest

	scaleResp *model.ScaleNodePoolResponse
	scaleErr  error
	scaleReqs []*model.ScaleNodePoolRequest
}

func (f *fakeAPI) ListNodePools(req *model.ListNodePoolsRequest) (*model.ListNodePoolsResponse, error) {
	f.listReqs = append(f.listReqs, req)
	return f.listResp, f.listErr
}

func (f *fakeAPI) UpdateNodePool(req *model.UpdateNodePoolRequest) (*model.UpdateNodePoolResponse, error) {
	f.updReqs = append(f.updReqs, req)
	return f.updResp, f.updErr
}

func (f *fakeAPI) ScaleNodePool(req *model.ScaleNodePoolRequest) (*model.ScaleNodePoolResponse, error) {
	f.scaleReqs = append(f.scaleReqs, req)
	return f.scaleResp, f.scaleErr
}

func i32(v int32) *int32   { return &v }
func str(v string) *string { return &v }

// pool builds a NodePoolResp with the given name, uid and current node count.
func pool(name, uid string, current int32) model.NodePoolResp {
	return model.NodePoolResp{
		Metadata: &model.NodePoolMetadata{Name: name, Uid: str(uid)},
		Status:   &model.NodePoolStatus{CurrentNode: i32(current)},
		Spec:     &model.NodePoolSpec{InitialNodeCount: i32(current)},
	}
}

func listOf(pools ...model.NodePoolResp) *model.ListNodePoolsResponse {
	return &model.ListNodePoolsResponse{Items: &pools}
}

// extGroup describes an extension scale group for the test pool builder.
type extGroup struct {
	name, az, flavor string
	count            int32
	hasStatus        bool // when false, omit the status entry (group with 0 nodes)
}

// scaleGroupPool builds a multi-group node pool: a default group (from the node
// template) plus extension scale groups, with matching status entries. total is
// the pool-wide CurrentNode (set independently to allow inconsistent-data tests).
func scaleGroupPool(name, uid string, total int32, defAZ, defFlavor string, defCount int32, ext ...extGroup) model.NodePoolResp {
	groups := make([]model.ExtensionScaleGroup, 0, len(ext))
	statuses := []model.ScaleGroupStatus{{
		Name:              str(defaultGroupName),
		DesiredNodeCount:  i32(defCount),
		ExistingNodeCount: &model.ScaleGroupStatusExistingNodeCount{Total: i32(defCount)},
	}}
	for _, e := range ext {
		groups = append(groups, model.ExtensionScaleGroup{
			Metadata: &model.ExtensionScaleGroupMetadata{Name: str(e.name)},
			Spec:     &model.ExtensionScaleGroupSpec{Az: str(e.az), Flavor: str(e.flavor)},
		})
		if e.hasStatus {
			statuses = append(statuses, model.ScaleGroupStatus{
				Name:              str(e.name),
				DesiredNodeCount:  i32(e.count),
				ExistingNodeCount: &model.ScaleGroupStatusExistingNodeCount{Total: i32(e.count)},
			})
		}
	}
	np := model.NodePoolResp{
		Metadata: &model.NodePoolMetadata{Name: name, Uid: str(uid)},
		Spec: &model.NodePoolSpec{
			NodeTemplate: &model.NodeTemplate{Az: str(defAZ), Flavor: str(defFlavor)},
		},
		Status: &model.NodePoolStatus{CurrentNode: i32(total), ScaleGroupStatuses: &statuses},
	}
	if len(groups) > 0 {
		np.Spec.ExtensionScaleGroups = &groups
	}
	return np
}

func TestScale_ResolvesNameAndScalesUp(t *testing.T) {
	f := &fakeAPI{listResp: listOf(pool("prod", "np-123", 0))}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 3,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if !res.Changed || res.Previous != 0 || res.Desired != 3 || res.NodePoolID != "np-123" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(f.updReqs) != 1 {
		t.Fatalf("expected 1 UpdateNodePool call, got %d", len(f.updReqs))
	}
	upd := f.updReqs[0]
	if upd.ClusterId != "c1" || upd.NodepoolId != "np-123" {
		t.Fatalf("wrong update target: %+v", upd)
	}
	if upd.Body == nil || upd.Body.Spec == nil || upd.Body.Spec.InitialNodeCount != 3 {
		t.Fatalf("wrong InitialNodeCount: %+v", upd.Body)
	}
	if upd.Body.Spec.Autoscaling != nil {
		t.Fatal("autoscaling must be left untouched (nil)")
	}
	if upd.Body.Metadata == nil || upd.Body.Metadata.Name == nil || *upd.Body.Metadata.Name != "prod" {
		t.Fatalf("name not set on update body: %+v", upd.Body.Metadata)
	}
}

func TestScale_ScaleToZero(t *testing.T) {
	f := &fakeAPI{listResp: listOf(pool("prod", "np-123", 3))}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 0,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if !res.Changed || res.Previous != 3 || res.Desired != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if f.updReqs[0].Body.Spec.InitialNodeCount != 0 {
		t.Fatalf("expected InitialNodeCount 0, got %d", f.updReqs[0].Body.Spec.InitialNodeCount)
	}
}

func TestScale_IdempotentSkip(t *testing.T) {
	f := &fakeAPI{listResp: listOf(pool("prod", "np-123", 3))}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 3,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if res.Changed {
		t.Fatal("expected Changed=false when already at desired count")
	}
	if len(f.updReqs) != 0 {
		t.Fatalf("expected no UpdateNodePool call, got %d", len(f.updReqs))
	}
}

func TestScale_ExplicitIDSkipsList(t *testing.T) {
	f := &fakeAPI{}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolID: "np-explicit", NodePoolName: "ignored", Desired: 2,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if len(f.listReqs) != 0 {
		t.Fatal("ListNodePools must not be called when NodePoolID is given")
	}
	if len(f.updReqs) != 1 || f.updReqs[0].NodepoolId != "np-explicit" {
		t.Fatalf("unexpected update reqs: %+v", f.updReqs)
	}
	if res.NodePoolID != "np-explicit" || res.Previous != -1 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestScale_DryRunDoesNotUpdate(t *testing.T) {
	f := &fakeAPI{listResp: listOf(pool("prod", "np-123", 0))}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 3, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if !res.Changed || !res.DryRun {
		t.Fatalf("dry-run should report intended change: %+v", res)
	}
	if len(f.updReqs) != 0 {
		t.Fatal("dry-run must not call UpdateNodePool")
	}
}

func TestScale_NotFound(t *testing.T) {
	f := &fakeAPI{listResp: listOf(pool("other", "np-1", 1))}
	s := &Scaler{api: f}

	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestScale_AmbiguousName(t *testing.T) {
	f := &fakeAPI{listResp: listOf(pool("prod", "np-1", 1), pool("prod", "np-2", 2))}
	s := &Scaler{api: f}

	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected ambiguous-name error")
	}
}

func TestScale_EmptyItems(t *testing.T) {
	f := &fakeAPI{listResp: &model.ListNodePoolsResponse{Items: nil}}
	s := &Scaler{api: f}

	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected error when Items is nil")
	}
}

func TestScale_ListError(t *testing.T) {
	f := &fakeAPI{listErr: errors.New("boom")}
	s := &Scaler{api: f}

	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected list error to propagate")
	}
}

func TestScale_UpdateError(t *testing.T) {
	f := &fakeAPI{
		listResp: listOf(pool("prod", "np-123", 0)),
		updErr:   errors.New("api rejected"),
	}
	s := &Scaler{api: f}

	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 3,
	})
	if err == nil {
		t.Fatal("expected update error to propagate")
	}
}

func TestScale_InvalidRequest(t *testing.T) {
	s := &Scaler{api: &fakeAPI{}}
	_, err := s.Scale(context.Background(), scaler.Request{Desired: 1})
	if err == nil {
		t.Fatal("expected validation error for missing cluster/nodepool")
	}
}

func TestScale_PreviousFallsBackToSpec(t *testing.T) {
	// Status missing → currentCount falls back to spec.InitialNodeCount.
	np := model.NodePoolResp{
		Metadata: &model.NodePoolMetadata{Name: "prod", Uid: str("np-9")},
		Spec:     &model.NodePoolSpec{InitialNodeCount: i32(5)},
	}
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 5,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if res.Previous != 5 || res.Changed {
		t.Fatalf("expected idempotent skip from spec fallback: %+v", res)
	}
}

func TestBuildCredential(t *testing.T) {
	tests := []struct {
		name    string
		creds   scaler.Creds
		wantErr bool
	}{
		{"permanent", scaler.Creds{AK: "ak", SK: "sk", ProjectID: "p"}, false},
		{"temporary with token", scaler.Creds{AK: "ak", SK: "sk", ProjectID: "p", SecurityToken: "tok"}, false},
		{"missing ak", scaler.Creds{SK: "sk"}, true},
		{"missing sk", scaler.Creds{AK: "ak"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred, err := buildCredential(tt.creds)
			if (err != nil) != tt.wantErr {
				t.Fatalf("buildCredential err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && cred == nil {
				t.Fatal("expected non-nil credentials")
			}
		})
	}
}

func TestNew_RequiresRegionOrEndpoint(t *testing.T) {
	_, err := New(scaler.Creds{AK: "ak", SK: "sk", ProjectID: "p"}, scaler.Options{})
	if err == nil {
		t.Fatal("expected error when neither region nor endpoint is set")
	}
}

func TestNew_WithValidRegion(t *testing.T) {
	s, err := New(scaler.Creds{AK: "ak", SK: "sk", ProjectID: "p"},
		scaler.Options{Region: "cn-north-4"})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil scaler")
	}
}

func TestNew_InvalidRegion(t *testing.T) {
	_, err := New(scaler.Creds{AK: "ak", SK: "sk", ProjectID: "p"},
		scaler.Options{Region: "no-such-region-xyz"})
	if err == nil {
		t.Fatal("expected error for unknown region")
	}
}

func TestNew_MissingCreds(t *testing.T) {
	_, err := New(scaler.Creds{}, scaler.Options{Region: "cn-north-4"})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
}

func TestScale_NilUid(t *testing.T) {
	np := model.NodePoolResp{
		Metadata: &model.NodePoolMetadata{Name: "prod"}, // Uid nil
		Status:   &model.NodePoolStatus{CurrentNode: i32(1)},
	}
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 2,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if res.NodePoolID != "" {
		t.Fatalf("expected empty NodePoolID for nil uid, got %q", res.NodePoolID)
	}
}

func TestScale_NoStatusNoSpec(t *testing.T) {
	// Neither status nor spec → currentCount returns -1 (unknown), so a scale
	// proceeds rather than being skipped.
	np := model.NodePoolResp{Metadata: &model.NodePoolMetadata{Name: "prod", Uid: str("np-x")}}
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "prod", Desired: 2,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if res.Previous != -1 || !res.Changed {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestNew_WithEndpoint(t *testing.T) {
	s, err := New(scaler.Creds{AK: "ak", SK: "sk", ProjectID: "p"},
		scaler.Options{Endpoint: "https://cce.example.com"})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil scaler")
	}
}

// --- per-scale-group scaling ---

// az3 staging-like pool: default group AZ1, plus AZ2/AZ3 extension groups.
func stagingPool() model.NodePoolResp {
	return scaleGroupPool("np", "np-1", 3, "ap-southeast-4a", "s7n.xlarge.2", 2,
		extGroup{name: "g-az2", az: "ap-southeast-4b", flavor: "s7n.xlarge.2", count: 0, hasStatus: true},
		extGroup{name: "g-az3", az: "ap-southeast-4c", flavor: "s7n.xlarge.2", count: 1, hasStatus: true},
	)
}

func scaleReqOf(t *testing.T, f *fakeAPI) *model.ScaleNodePoolRequest {
	t.Helper()
	if len(f.scaleReqs) != 1 {
		t.Fatalf("expected 1 ScaleNodePool call, got %d", len(f.scaleReqs))
	}
	return f.scaleReqs[0]
}

func TestScaleGroup_ByName_Up(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "g-az3", Desired: 3,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if !res.Changed || res.ScaleGroup != "g-az3" || res.Previous != 1 || res.Desired != 3 {
		t.Fatalf("unexpected result: %+v", res)
	}
	req := scaleReqOf(t, f)
	spec := req.Body.Spec
	// delta = 3-1 = 2; newTotal = poolTotal(3) + 2 = 5
	if spec.DesiredNodeCount != 5 {
		t.Fatalf("DesiredNodeCount = %d, want 5", spec.DesiredNodeCount)
	}
	if len(spec.ScaleGroups) != 1 || spec.ScaleGroups[0] != "g-az3" {
		t.Fatalf("ScaleGroups = %v, want [g-az3]", spec.ScaleGroups)
	}
	if spec.Options == nil || spec.Options.ScalePolicy.Value() != "Random" {
		t.Fatalf("expected default Random policy, got %+v", spec.Options)
	}
	if len(f.updReqs) != 0 {
		t.Fatal("UpdateNodePool must not be called on the group path")
	}
}

func TestScaleGroup_ByAZ_Resolves(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", AZ: "ap-southeast-4c", Desired: 2,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if res.ScaleGroup != "g-az3" {
		t.Fatalf("AZ should resolve to g-az3, got %q", res.ScaleGroup)
	}
	// delta = 2-1 = 1; newTotal = 3 + 1 = 4
	if scaleReqOf(t, f).Body.Spec.DesiredNodeCount != 4 {
		t.Fatalf("DesiredNodeCount = %d, want 4", f.scaleReqs[0].Body.Spec.DesiredNodeCount)
	}
}

func TestScaleGroup_ToZero(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "g-az3", Desired: 0,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if !res.Changed {
		t.Fatal("expected change")
	}
	// delta = 0-1 = -1; newTotal = 3 - 1 = 2 (other groups untouched)
	if scaleReqOf(t, f).Body.Spec.DesiredNodeCount != 2 {
		t.Fatalf("DesiredNodeCount = %d, want 2", f.scaleReqs[0].Body.Spec.DesiredNodeCount)
	}
}

func TestScaleGroup_Idempotent(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "g-az3", Desired: 1, // already 1
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if res.Changed {
		t.Fatal("expected no change when group already at desired")
	}
	if len(f.scaleReqs) != 0 {
		t.Fatal("ScaleNodePool must not be called when idempotent")
	}
}

func TestScaleGroup_DryRun(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "g-az3", Desired: 5, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if !res.Changed || !res.DryRun {
		t.Fatalf("dry-run should report intended change: %+v", res)
	}
	if len(f.scaleReqs) != 0 {
		t.Fatal("dry-run must not call ScaleNodePool")
	}
}

func TestScaleGroup_AZBalancePolicy(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}

	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "g-az3", Desired: 2,
		ScalePolicy: scaler.ScalePolicyAZBalance,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if scaleReqOf(t, f).Body.Spec.Options.ScalePolicy.Value() != "AZBalance" {
		t.Fatal("expected AZBalance policy")
	}
}

func TestScaleGroup_NameNotFound(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}
	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "nope", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestScaleGroup_AZAmbiguous(t *testing.T) {
	// Two groups in the same AZ with different flavors → ambiguous without flavor.
	np := scaleGroupPool("np", "np-1", 2, "az-a", "flavorA", 1,
		extGroup{name: "g-big", az: "az-a", flavor: "flavorB", count: 1, hasStatus: true},
	)
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}

	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", AZ: "az-a", Desired: 2,
	})
	if err == nil {
		t.Fatal("expected ambiguous-AZ error")
	}

	// Adding the flavor disambiguates.
	f2 := &fakeAPI{listResp: listOf(np)}
	s2 := &Scaler{api: f2}
	res, err := s2.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", AZ: "az-a", Flavor: "flavorB", Desired: 2,
	})
	if err != nil {
		t.Fatalf("flavor should disambiguate: %v", err)
	}
	if res.ScaleGroup != "g-big" {
		t.Fatalf("expected g-big, got %q", res.ScaleGroup)
	}
}

func TestScaleGroup_MultiAZDefaultByAZ(t *testing.T) {
	// Default group is multi-AZ (az random); targeting a specific AZ fails clearly.
	np := scaleGroupPool("np", "np-1", 3, "random", "s7n.xlarge.2", 3)
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}

	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", AZ: "ap-southeast-4c", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected error targeting a single AZ on a multi-AZ group")
	}
}

func TestScaleGroup_MissingStatusTreatedAsZero(t *testing.T) {
	// g-new exists in spec but has no status entry → current count 0.
	np := scaleGroupPool("np", "np-1", 2, "az-a", "flavorA", 2,
		extGroup{name: "g-new", az: "az-b", flavor: "flavorA", count: 0, hasStatus: false},
	)
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}

	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "g-new", Desired: 2,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if res.Previous != 0 {
		t.Fatalf("expected current 0 for group without status, got %d", res.Previous)
	}
	// delta = 2-0 = 2; newTotal = 2 + 2 = 4
	if scaleReqOf(t, f).Body.Spec.DesiredNodeCount != 4 {
		t.Fatalf("DesiredNodeCount = %d, want 4", f.scaleReqs[0].Body.Spec.DesiredNodeCount)
	}
}

func TestScaleGroup_NegativeTotalGuard(t *testing.T) {
	// Inconsistent data: pool total 1 but group desired 3; scaling group to 0
	// would compute newTotal = 1 - 3 = -2 → guarded error.
	np := scaleGroupPool("np", "np-1", 1, "az-a", "flavorA", 0,
		extGroup{name: "g-x", az: "az-b", flavor: "flavorA", count: 3, hasStatus: true},
	)
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}

	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "g-x", Desired: 0,
	})
	if err == nil {
		t.Fatal("expected negative-total guard error")
	}
}

func TestScaleGroup_ScaleNodePoolError(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool()), scaleErr: errors.New("api rejected")}
	s := &Scaler{api: f}
	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "g-az3", Desired: 3,
	})
	if err == nil {
		t.Fatal("expected ScaleNodePool error to propagate")
	}
}

func TestScaleGroup_ByExplicitID(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}
	res, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolID: "np-1", ScaleGroup: "g-az3", Desired: 2,
	})
	if err != nil {
		t.Fatalf("Scale error: %v", err)
	}
	if res.NodePoolID != "np-1" || res.ScaleGroup != "g-az3" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestScaleGroup_PoolNotFoundByID(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}
	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolID: "missing", ScaleGroup: "g-az3", Desired: 2,
	})
	if err == nil {
		t.Fatal("expected pool-not-found error")
	}
}

func TestScaleGroup_PoolMissingStatus(t *testing.T) {
	// Pool has no status → poolTotal unreadable → error.
	np := model.NodePoolResp{
		Metadata: &model.NodePoolMetadata{Name: "np", Uid: str("np-1")},
		Spec: &model.NodePoolSpec{
			NodeTemplate: &model.NodeTemplate{Az: str("az-a"), Flavor: str("f")},
		},
	}
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}
	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "default", Desired: 5,
	})
	if err == nil {
		t.Fatal("expected error when pool total cannot be read")
	}
}

func TestScaleGroup_ListError(t *testing.T) {
	f := &fakeAPI{listErr: errors.New("boom")}
	s := &Scaler{api: f}
	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", ScaleGroup: "default", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected list error to propagate")
	}
}

// --- Describe ---

func TestDescribe(t *testing.T) {
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}

	info, err := s.Describe(context.Background(), scaler.Request{Cluster: "c1", NodePoolName: "np"})
	if err != nil {
		t.Fatalf("Describe error: %v", err)
	}
	if info.Name != "np" || info.ID != "np-1" || info.Total != 3 {
		t.Fatalf("unexpected pool info: %+v", info)
	}
	if len(info.Groups) != 3 {
		t.Fatalf("expected 3 groups (default + 2 ext), got %d", len(info.Groups))
	}
	// default group first, from node template
	if info.Groups[0].Name != "default" || info.Groups[0].AZ != "ap-southeast-4a" || info.Groups[0].Desired != 2 {
		t.Fatalf("unexpected default group: %+v", info.Groups[0])
	}
	// find g-az3
	var az3 *scaler.ScaleGroupInfo
	for i := range info.Groups {
		if info.Groups[i].Name == "g-az3" {
			az3 = &info.Groups[i]
		}
	}
	if az3 == nil || az3.AZ != "ap-southeast-4c" || az3.Existing != 1 {
		t.Fatalf("unexpected g-az3: %+v", az3)
	}
}

func TestDescribe_ListError(t *testing.T) {
	f := &fakeAPI{listErr: errors.New("boom")}
	s := &Scaler{api: f}
	_, err := s.Describe(context.Background(), scaler.Request{Cluster: "c1", NodePoolName: "np"})
	if err == nil {
		t.Fatal("expected list error to propagate")
	}
}

// Scaler implements both Scaler and Describer.
var _ scaler.Scaler = (*Scaler)(nil)
var _ scaler.Describer = (*Scaler)(nil)

func bptr(v bool) *bool { return &v }

func TestScaleGroup_AZNoMatchNoMultiAZ(t *testing.T) {
	// All groups are single-AZ; an unknown AZ yields the plain no-match error.
	f := &fakeAPI{listResp: listOf(stagingPool())}
	s := &Scaler{api: f}
	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", AZ: "no-such-az", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected no-match error")
	}
}

func TestScaleGroup_EmptyAZDefaultByAZ(t *testing.T) {
	// Default group AZ is empty (multi-AZ); targeting a specific AZ fails and the
	// error renders the empty AZ as "random".
	np := scaleGroupPool("np", "np-1", 2, "", "s7n.xlarge.2", 2)
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}
	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolName: "np", AZ: "az-x", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected error for empty-AZ default group targeted by AZ")
	}
}

func TestScaleGroup_PoolNilItemsByID(t *testing.T) {
	f := &fakeAPI{listResp: &model.ListNodePoolsResponse{Items: nil}}
	s := &Scaler{api: f}
	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolID: "np-1", ScaleGroup: "default", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected not-found error when Items is nil on ID path")
	}
}

func TestDescribe_AutoscalingAndMissingStatus(t *testing.T) {
	// Default group with autoscaling enabled; one ext group enabled; one ext group
	// without a status entry (existing/desired fall back to 0).
	groups := []model.ExtensionScaleGroup{
		{
			Metadata: &model.ExtensionScaleGroupMetadata{Name: str("g-on")},
			Spec: &model.ExtensionScaleGroupSpec{
				Az: str("az-b"), Flavor: str("f"),
				Autoscaling: &model.ScaleGroupAutoscaling{Enable: bptr(true)},
			},
		},
		{
			Metadata: &model.ExtensionScaleGroupMetadata{Name: str("g-nostatus")},
			Spec:     &model.ExtensionScaleGroupSpec{Az: str("az-c"), Flavor: str("f")},
		},
	}
	statuses := []model.ScaleGroupStatus{
		{Name: str("default"), DesiredNodeCount: i32(1), ExistingNodeCount: &model.ScaleGroupStatusExistingNodeCount{Total: i32(1)}},
		{Name: str("g-on"), DesiredNodeCount: i32(2), ExistingNodeCount: &model.ScaleGroupStatusExistingNodeCount{Total: i32(2)}},
	}
	np := model.NodePoolResp{
		Metadata: &model.NodePoolMetadata{Name: "np", Uid: str("np-1")},
		Spec: &model.NodePoolSpec{
			NodeTemplate: &model.NodeTemplate{Az: str("az-a"), Flavor: str("f")},
			Autoscaling:  &model.NodePoolNodeAutoscaling{Enable: bptr(true)},
			ExtensionScaleGroups: &groups,
		},
		Status: &model.NodePoolStatus{CurrentNode: i32(3), ScaleGroupStatuses: &statuses},
	}
	f := &fakeAPI{listResp: listOf(np)}
	s := &Scaler{api: f}

	info, err := s.Describe(context.Background(), scaler.Request{Cluster: "c1", NodePoolName: "np"})
	if err != nil {
		t.Fatalf("Describe error: %v", err)
	}
	byName := map[string]scaler.ScaleGroupInfo{}
	for _, g := range info.Groups {
		byName[g.Name] = g
	}
	if !byName["default"].Autoscaling || !byName["g-on"].Autoscaling {
		t.Fatalf("expected autoscaling true for default and g-on: %+v", info.Groups)
	}
	if g := byName["g-nostatus"]; g.Desired != 0 || g.Existing != 0 || g.Autoscaling {
		t.Fatalf("g-nostatus should be zero/false: %+v", g)
	}
}

func TestScaleGroup_ListErrorByID(t *testing.T) {
	f := &fakeAPI{listErr: errors.New("boom")}
	s := &Scaler{api: f}
	_, err := s.Scale(context.Background(), scaler.Request{
		Cluster: "c1", NodePoolID: "np-1", ScaleGroup: "default", Desired: 1,
	})
	if err == nil {
		t.Fatal("expected list error to propagate on ID path")
	}
}
