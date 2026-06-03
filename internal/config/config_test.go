package config

import (
	"testing"

	"github.com/rahadiangg/siesta/internal/scaler"
)

func TestSettingsFromEnv_Defaults(t *testing.T) {
	s := SettingsFromEnv(MapGetenv(map[string]string{}))
	if s.Provider != DefaultProvider {
		t.Fatalf("expected default provider, got %q", s.Provider)
	}
}

func TestSettingsFromEnv_Values(t *testing.T) {
	s := SettingsFromEnv(MapGetenv(map[string]string{
		"SIESTA_PROVIDER":    "huawei-cce",
		"SIESTA_REGION":      "cn-north-4",
		"SIESTA_ENDPOINT":    "https://cce.example.com",
		"SIESTA_CLUSTER":     "c1",
		"SIESTA_NODEPOOL":    "prod",
		"SIESTA_NODEPOOL_ID": "np-1",
	}))
	want := Settings{
		Provider: "huawei-cce", Region: "cn-north-4", Endpoint: "https://cce.example.com",
		Cluster: "c1", NodePoolName: "prod", NodePoolID: "np-1",
	}
	if s != want {
		t.Fatalf("got %+v, want %+v", s, want)
	}
}

func TestSettingsFromEnv_RegionFallback(t *testing.T) {
	s := SettingsFromEnv(MapGetenv(map[string]string{"HUAWEICLOUD_REGION": "ap-southeast-3"}))
	if s.Region != "ap-southeast-3" {
		t.Fatalf("expected HUAWEICLOUD_REGION fallback, got %q", s.Region)
	}
}

func TestSettings_Options(t *testing.T) {
	s := Settings{Region: "r", Endpoint: "e"}
	o := s.Options()
	if o.Region != "r" || o.Endpoint != "e" {
		t.Fatalf("unexpected options: %+v", o)
	}
}

func TestCredsFromEnv(t *testing.T) {
	c := CredsFromEnv(MapGetenv(map[string]string{
		"HUAWEICLOUD_SDK_AK":             "ak",
		"HUAWEICLOUD_SDK_SK":             "sk",
		"HUAWEICLOUD_PROJECT_ID":         "p",
		"HUAWEICLOUD_SDK_SECURITY_TOKEN": "tok",
	}))
	if c.AK != "ak" || c.SK != "sk" || c.ProjectID != "p" || c.SecurityToken != "tok" {
		t.Fatalf("unexpected creds: %+v", c)
	}
}

func TestMergeCreds(t *testing.T) {
	primary := scaler.Creds{AK: "pak", SK: "psk", SecurityToken: "ptok"} // no ProjectID
	fallback := scaler.Creds{AK: "fak", SK: "fsk", ProjectID: "fp", SecurityToken: "ftok"}
	got := MergeCreds(primary, fallback)
	want := scaler.Creds{AK: "pak", SK: "psk", ProjectID: "fp", SecurityToken: "ptok"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseUserEvent(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		count   int32
	}{
		{"valid", `{"nodePool":"prod","count":3}`, false, 3},
		{"zero count", `{"nodePool":"prod","count":0}`, false, 0},
		{"empty", ``, true, 0},
		{"malformed", `{not json`, true, 0},
		{"missing count", `{"nodePool":"prod"}`, true, 0},
		{"negative count", `{"nodePool":"prod","count":-1}`, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := ParseUserEvent(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && (ev.Count == nil || *ev.Count != tt.count) {
				t.Fatalf("count = %v, want %d", ev.Count, tt.count)
			}
		})
	}
}

func TestUserEvent_Request_OverlaysBase(t *testing.T) {
	base := Settings{Cluster: "base-cluster", NodePoolName: "base-np"}
	ev, err := ParseUserEvent(`{"count":2}`)
	if err != nil {
		t.Fatal(err)
	}
	r := ev.Request(base)
	if r.Cluster != "base-cluster" || r.NodePoolName != "base-np" || r.Desired != 2 {
		t.Fatalf("unexpected request: %+v", r)
	}
}

func TestUserEvent_Request_ScaleGroupFields(t *testing.T) {
	base := Settings{Cluster: "base-cluster", NodePoolName: "base-np"}
	ev, err := ParseUserEvent(`{"az":"ap-southeast-4c","flavor":"s7n.xlarge.2","scalePolicy":"azbalance","count":1}`)
	if err != nil {
		t.Fatal(err)
	}
	r := ev.Request(base)
	if r.AZ != "ap-southeast-4c" || r.Flavor != "s7n.xlarge.2" || r.ScalePolicy != "azbalance" || r.Desired != 1 {
		t.Fatalf("scale-group fields not mapped: %+v", r)
	}
	if !r.TargetsGroup() {
		t.Fatal("expected TargetsGroup() true")
	}
}

func TestUserEvent_Request_ScaleGroupByName(t *testing.T) {
	ev, err := ParseUserEvent(`{"scaleGroup":"g-az3","count":2}`)
	if err != nil {
		t.Fatal(err)
	}
	r := ev.Request(Settings{Cluster: "c1", NodePoolName: "np"})
	if r.ScaleGroup != "g-az3" || r.Desired != 2 {
		t.Fatalf("unexpected request: %+v", r)
	}
}

func TestUserEvent_Request_EventOverrides(t *testing.T) {
	base := Settings{Cluster: "base-cluster", NodePoolName: "base-np"}
	ev, err := ParseUserEvent(`{"cluster":"ev-cluster","nodePool":"ev-np","count":5,"dryRun":true}`)
	if err != nil {
		t.Fatal(err)
	}
	r := ev.Request(base)
	if r.Cluster != "ev-cluster" || r.NodePoolName != "ev-np" || r.Desired != 5 || !r.DryRun {
		t.Fatalf("unexpected request: %+v", r)
	}
}
