package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rahadiangg/siesta/internal/config"
	"github.com/rahadiangg/siesta/internal/scaler"
)

type fakeScaler struct {
	res    scaler.Result
	err    error
	gotReq scaler.Request
	called bool

	info        scaler.PoolInfo
	describeErr error
}

func (f *fakeScaler) Scale(_ context.Context, r scaler.Request) (scaler.Result, error) {
	f.called = true
	f.gotReq = r
	return f.res, f.err
}

func (f *fakeScaler) Describe(_ context.Context, r scaler.Request) (scaler.PoolInfo, error) {
	f.gotReq = r
	return f.info, f.describeErr
}

// scaleOnly implements scaler.Scaler but NOT scaler.Describer.
type scaleOnly struct{}

func (scaleOnly) Scale(context.Context, scaler.Request) (scaler.Result, error) {
	return scaler.Result{}, nil
}

// withScaler swaps newScaler for the duration of a test.
func withScaler(t *testing.T, f func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error)) {
	t.Helper()
	old := newScaler
	newScaler = f
	t.Cleanup(func() { newScaler = old })
}

func env(m map[string]string) config.Getenv { return config.MapGetenv(m) }

func TestMain_Success(t *testing.T) {
	fs := &fakeScaler{res: scaler.Result{Provider: "huawei-cce", NodePoolID: "np-1", Previous: 0, Desired: 3, Changed: true}}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	code := Main([]string{"--cluster", "c1", "--nodepool", "prod", "--count", "3"}, env(nil), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, errOut.String())
	}
	if !fs.called || fs.gotReq.Desired != 3 || fs.gotReq.Cluster != "c1" {
		t.Fatalf("unexpected request: %+v", fs.gotReq)
	}
	if !strings.Contains(out.String(), "scaled") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestMain_JSONOutput(t *testing.T) {
	fs := &fakeScaler{res: scaler.Result{Provider: "huawei-cce", NodePoolID: "np-1", Desired: 0, Changed: true}}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	code := Main([]string{"--cluster", "c1", "--nodepool", "prod", "--count", "0", "--json"}, env(nil), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, errOut.String())
	}
	var res scaler.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if res.Provider != "huawei-cce" {
		t.Fatalf("unexpected json result: %+v", res)
	}
}

func TestMain_NoChange(t *testing.T) {
	fs := &fakeScaler{res: scaler.Result{Changed: false, Desired: 3, Previous: 3}}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	Main([]string{"--cluster", "c1", "--nodepool", "prod", "--count", "3"}, env(nil), &out, &errOut)
	if !strings.Contains(out.String(), "no change") {
		t.Fatalf("expected no-change message, got %q", out.String())
	}
}

func TestMain_DryRun(t *testing.T) {
	fs := &fakeScaler{res: scaler.Result{DryRun: true, Changed: true, Desired: 3}}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	Main([]string{"--cluster", "c1", "--nodepool", "prod", "--count", "3", "--dry-run"}, env(nil), &out, &errOut)
	if !strings.Contains(out.String(), "dry-run") {
		t.Fatalf("expected dry-run message, got %q", out.String())
	}
}

func TestMain_ScaleError(t *testing.T) {
	fs := &fakeScaler{err: errors.New("boom")}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	code := Main([]string{"--cluster", "c1", "--nodepool", "prod", "--count", "3"}, env(nil), &out, &errOut)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("expected error on stderr, got %q", errOut.String())
	}
}

func TestMain_BuildError(t *testing.T) {
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) {
		return nil, errors.New("bad creds")
	})
	var out, errOut bytes.Buffer
	code := Main([]string{"--cluster", "c1", "--nodepool", "prod", "--count", "3"}, env(nil), &out, &errOut)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
}

func TestMain_MissingCount(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"--cluster", "c1", "--nodepool", "prod"}, env(nil), &out, &errOut)
	if code != 2 {
		t.Fatalf("expected exit 2 for missing count, got %d", code)
	}
}

func TestMain_BadFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"--nope"}, env(nil), &out, &errOut)
	if code != 2 {
		t.Fatalf("expected exit 2 for bad flag, got %d", code)
	}
}

func TestMain_InvalidRequest(t *testing.T) {
	// count provided but no cluster/nodepool → request validation fails (exit 2).
	var out, errOut bytes.Buffer
	code := Main([]string{"--count", "3"}, env(nil), &out, &errOut)
	if code != 2 {
		t.Fatalf("expected exit 2 for invalid request, got %d", code)
	}
}

func TestMain_EnvDefaults(t *testing.T) {
	fs := &fakeScaler{}
	var gotProvider string
	var gotOpts scaler.Options
	newScalerOld := newScaler
	newScaler = func(name string, _ scaler.Creds, o scaler.Options) (scaler.Scaler, error) {
		gotProvider, gotOpts = name, o
		return fs, nil
	}
	t.Cleanup(func() { newScaler = newScalerOld })

	getenv := env(map[string]string{
		"SIESTA_CLUSTER":  "env-cluster",
		"SIESTA_NODEPOOL": "env-np",
		"SIESTA_REGION":   "cn-north-4",
	})
	var out, errOut bytes.Buffer
	code := Main([]string{"--count", "2"}, getenv, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if fs.gotReq.Cluster != "env-cluster" || fs.gotReq.NodePoolName != "env-np" || fs.gotReq.Desired != 2 {
		t.Fatalf("env defaults not applied: %+v", fs.gotReq)
	}
	if gotProvider != "huawei-cce" || gotOpts.Region != "cn-north-4" {
		t.Fatalf("unexpected provider/opts: %s %+v", gotProvider, gotOpts)
	}
}

func TestMain_ModeFlagAccepted(t *testing.T) {
	fs := &fakeScaler{}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	code := Main([]string{"--mode", "cli", "--cluster", "c", "--nodepool", "n", "--count", "1"}, env(nil), &out, &errOut)
	if code != 0 {
		t.Fatalf("--mode should be accepted: exit=%d stderr=%s", code, errOut.String())
	}
}

func TestMain_Help(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"--help"}, env(nil), &out, &errOut)
	if code != 0 {
		t.Fatalf("--help should exit 0, got %d", code)
	}
	if !strings.Contains(out.String(), "Usage:") || !strings.Contains(out.String(), "siesta") {
		t.Fatalf("help output missing usage: %q", out.String())
	}
}

// --- scale-group flags ---

func TestMain_ScaleGroupByName(t *testing.T) {
	fs := &fakeScaler{res: scaler.Result{Provider: "huawei-cce", NodePoolID: "np-1", ScaleGroup: "g-az3", Previous: 1, Desired: 3, Changed: true}}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	code := Main([]string{"--cluster", "c1", "--nodepool", "np", "--scale-group", "g-az3", "--count", "3"}, env(nil), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if fs.gotReq.ScaleGroup != "g-az3" {
		t.Fatalf("ScaleGroup not passed: %+v", fs.gotReq)
	}
	if !strings.Contains(out.String(), "group=g-az3") {
		t.Fatalf("output should mention the group: %q", out.String())
	}
}

func TestMain_ScaleByAZ(t *testing.T) {
	fs := &fakeScaler{res: scaler.Result{Provider: "huawei-cce", NodePoolID: "np-1", ScaleGroup: "g-az3", Changed: true}}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	code := Main([]string{"--cluster", "c1", "--nodepool", "np", "--az", "ap-southeast-4c", "--flavor", "s7n.xlarge.2", "--scale-policy", "azbalance", "--count", "1"}, env(nil), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if fs.gotReq.AZ != "ap-southeast-4c" || fs.gotReq.Flavor != "s7n.xlarge.2" || fs.gotReq.ScalePolicy != "azbalance" {
		t.Fatalf("AZ/flavor/policy not passed: %+v", fs.gotReq)
	}
}

func TestMain_InvalidScalePolicy(t *testing.T) {
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return &fakeScaler{}, nil })
	var out, errOut bytes.Buffer
	code := Main([]string{"--cluster", "c1", "--nodepool", "np", "--scale-group", "g", "--scale-policy", "bogus", "--count", "1"}, env(nil), &out, &errOut)
	if code != 2 {
		t.Fatalf("expected exit 2 for invalid scale policy, got %d", code)
	}
}

// --- describe subcommand ---

func describeInfo() scaler.PoolInfo {
	return scaler.PoolInfo{
		Name: "np", ID: "np-1", Total: 3,
		Groups: []scaler.ScaleGroupInfo{
			{Name: "default", AZ: "ap-southeast-4a", Flavor: "s7n.xlarge.2", Desired: 2, Existing: 2},
			{Name: "g-az3", AZ: "ap-southeast-4c", Flavor: "s7n.xlarge.2", Desired: 1, Existing: 1},
		},
	}
}

func TestDescribe_Human(t *testing.T) {
	fs := &fakeScaler{info: describeInfo()}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	code := Main([]string{"describe", "--cluster", "c1", "--nodepool", "np", "--region", "cn-north-4"}, env(nil), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "g-az3") || !strings.Contains(s, "ap-southeast-4c") || !strings.Contains(s, "GROUP") {
		t.Fatalf("describe output missing expected content: %q", s)
	}
	if fs.gotReq.Cluster != "c1" || fs.gotReq.NodePoolName != "np" {
		t.Fatalf("describe request wrong: %+v", fs.gotReq)
	}
}

func TestDescribe_JSON(t *testing.T) {
	fs := &fakeScaler{info: describeInfo()}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	var out, errOut bytes.Buffer
	code := Main([]string{"describe", "--cluster", "c1", "--nodepool", "np", "--json"}, env(nil), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	var info scaler.PoolInfo
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if len(info.Groups) != 2 {
		t.Fatalf("unexpected groups: %+v", info)
	}
}

func TestDescribe_MissingArgs(t *testing.T) {
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return &fakeScaler{}, nil })
	var out, errOut bytes.Buffer
	code := Main([]string{"describe", "--region", "cn-north-4"}, env(nil), &out, &errOut)
	if code != 2 {
		t.Fatalf("expected exit 2 for missing cluster/nodepool, got %d", code)
	}
}

func TestDescribe_Unsupported(t *testing.T) {
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return scaleOnly{}, nil })
	var out, errOut bytes.Buffer
	code := Main([]string{"describe", "--cluster", "c1", "--nodepool", "np"}, env(nil), &out, &errOut)
	if code != 1 {
		t.Fatalf("expected exit 1 for provider without describe, got %d", code)
	}
	if !strings.Contains(errOut.String(), "does not support describe") {
		t.Fatalf("unexpected error: %q", errOut.String())
	}
}

func TestDescribe_Error(t *testing.T) {
	fs := &fakeScaler{describeErr: errors.New("api down")}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })
	var out, errOut bytes.Buffer
	code := Main([]string{"describe", "--cluster", "c1", "--nodepool", "np"}, env(nil), &out, &errOut)
	if code != 1 {
		t.Fatalf("expected exit 1 on describe error, got %d", code)
	}
}

func TestDescribe_BuildError(t *testing.T) {
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) {
		return nil, errors.New("bad creds")
	})
	var out, errOut bytes.Buffer
	code := Main([]string{"describe", "--cluster", "c1", "--nodepool", "np"}, env(nil), &out, &errOut)
	if code != 1 {
		t.Fatalf("expected exit 1 on build error, got %d", code)
	}
}
