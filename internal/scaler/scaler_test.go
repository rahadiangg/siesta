package scaler

import (
	"context"
	"testing"
)

type stubScaler struct{}

func (stubScaler) Scale(context.Context, Request) (Result, error) { return Result{}, nil }

func TestRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{"valid with name", Request{Cluster: "c1", NodePoolName: "np", Desired: 3}, false},
		{"valid with id", Request{Cluster: "c1", NodePoolID: "id", Desired: 0}, false},
		{"missing cluster", Request{NodePoolName: "np", Desired: 1}, true},
		{"missing nodepool", Request{Cluster: "c1", Desired: 1}, true},
		{"negative desired", Request{Cluster: "c1", NodePoolName: "np", Desired: -1}, true},
		{"valid scale policy random", Request{Cluster: "c1", NodePoolName: "np", Desired: 1, ScalePolicy: ScalePolicyRandom}, false},
		{"valid scale policy azbalance", Request{Cluster: "c1", NodePoolName: "np", Desired: 1, ScalePolicy: ScalePolicyAZBalance}, false},
		{"invalid scale policy", Request{Cluster: "c1", NodePoolName: "np", Desired: 1, ScalePolicy: "bogus"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestRequestTargetsGroup(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want bool
	}{
		{"pool total", Request{Cluster: "c1", NodePoolName: "np"}, false},
		{"by name", Request{ScaleGroup: "default"}, true},
		{"by az", Request{AZ: "az1"}, true},
		{"by flavor", Request{Flavor: "s7n.xlarge.2"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.req.TargetsGroup(); got != tt.want {
				t.Fatalf("TargetsGroup() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	// Use a fresh registry to avoid cross-test contamination.
	old := registry
	registry = map[string]Constructor{}
	defer func() { registry = old }()

	Register("stub", func(Creds, Options) (Scaler, error) { return stubScaler{}, nil })

	if got := Providers(); len(got) != 1 || got[0] != "stub" {
		t.Fatalf("Providers() = %v, want [stub]", got)
	}

	s, err := New("stub", Creds{}, Options{})
	if err != nil {
		t.Fatalf("New(stub) error: %v", err)
	}
	if s == nil {
		t.Fatal("New(stub) returned nil scaler")
	}

	if _, err := New("missing", Creds{}, Options{}); err == nil {
		t.Fatal("New(missing) expected error, got nil")
	}
}
