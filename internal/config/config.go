// Package config loads non-secret settings and credentials from the
// environment, and parses the per-invocation FunctionGraph timer user_event
// payload. Everything takes an explicit getenv function so it is pure and
// unit-testable.
package config

import (
	"encoding/json"
	"fmt"

	"github.com/rahadiangg/siesta/internal/scaler"
)

// DefaultProvider is used when no provider is configured.
const DefaultProvider = "huawei-cce"

// Settings holds non-secret base configuration shared by all run modes.
type Settings struct {
	Provider     string
	Region       string
	Endpoint     string
	Cluster      string
	NodePoolName string
	NodePoolID   string
}

// Getenv looks up an environment variable. os.Getenv satisfies it; tests pass a
// map-backed function.
type Getenv func(string) string

// MapGetenv returns a Getenv backed by m (handy for tests).
func MapGetenv(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

// SettingsFromEnv reads base settings from the environment, applying defaults.
func SettingsFromEnv(getenv Getenv) Settings {
	provider := firstNonEmpty(getenv("SIESTA_PROVIDER"), DefaultProvider)
	return Settings{
		Provider:     provider,
		Region:       firstNonEmpty(getenv("SIESTA_REGION"), getenv("HUAWEICLOUD_REGION")),
		Endpoint:     getenv("SIESTA_ENDPOINT"),
		Cluster:      getenv("SIESTA_CLUSTER"),
		NodePoolName: getenv("SIESTA_NODEPOOL"),
		NodePoolID:   getenv("SIESTA_NODEPOOL_ID"),
	}
}

// Options converts Settings to scaler.Options.
func (s Settings) Options() scaler.Options {
	return scaler.Options{Region: s.Region, Endpoint: s.Endpoint}
}

// CredsFromEnv reads provider credentials from the environment.
func CredsFromEnv(getenv Getenv) scaler.Creds {
	return scaler.Creds{
		AK:            getenv("HUAWEICLOUD_SDK_AK"),
		SK:            getenv("HUAWEICLOUD_SDK_SK"),
		ProjectID:     getenv("HUAWEICLOUD_PROJECT_ID"),
		SecurityToken: getenv("HUAWEICLOUD_SDK_SECURITY_TOKEN"),
	}
}

// MergeCreds returns primary with any blank fields filled in from fallback. Used
// to prefer FunctionGraph agency credentials while falling back to env vars.
func MergeCreds(primary, fallback scaler.Creds) scaler.Creds {
	primary.AK = firstNonEmpty(primary.AK, fallback.AK)
	primary.SK = firstNonEmpty(primary.SK, fallback.SK)
	primary.ProjectID = firstNonEmpty(primary.ProjectID, fallback.ProjectID)
	primary.SecurityToken = firstNonEmpty(primary.SecurityToken, fallback.SecurityToken)
	return primary
}

// UserEvent is the per-invocation payload carried in a FunctionGraph timer's
// user_event field, e.g. {"nodePool":"prod","count":3}. Count is a pointer so an
// explicit 0 is distinguishable from "not provided".
type UserEvent struct {
	Cluster    string `json:"cluster"`
	NodePool   string `json:"nodePool"`
	NodePoolID string `json:"nodePoolId"`
	Count      *int32 `json:"count"`
	DryRun     bool   `json:"dryRun"`

	// Optional scale-group selector (Huawei CCE). When set, count targets that
	// group rather than the whole pool.
	ScaleGroup  string `json:"scaleGroup"`
	AZ          string `json:"az"`
	Flavor      string `json:"flavor"`
	ScalePolicy string `json:"scalePolicy"`
}

// ParseUserEvent parses the raw user_event JSON string. It requires count to be
// present.
func ParseUserEvent(raw string) (UserEvent, error) {
	var ev UserEvent
	if raw == "" {
		return ev, fmt.Errorf("user_event is empty")
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return ev, fmt.Errorf("parse user_event: %w", err)
	}
	if ev.Count == nil {
		return ev, fmt.Errorf("user_event must include a count")
	}
	if *ev.Count < 0 {
		return ev, fmt.Errorf("count must be >= 0, got %d", *ev.Count)
	}
	return ev, nil
}

// Request builds a scaler.Request from base settings, overlaying any fields the
// user_event specifies. The event count is required (guaranteed non-nil by
// ParseUserEvent).
func (ev UserEvent) Request(base Settings) scaler.Request {
	return scaler.Request{
		Cluster:      firstNonEmpty(ev.Cluster, base.Cluster),
		NodePoolName: firstNonEmpty(ev.NodePool, base.NodePoolName),
		NodePoolID:   firstNonEmpty(ev.NodePoolID, base.NodePoolID),
		Desired:      *ev.Count,
		DryRun:       ev.DryRun,
		ScaleGroup:   ev.ScaleGroup,
		AZ:           ev.AZ,
		Flavor:       ev.Flavor,
		ScalePolicy:  ev.ScalePolicy,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
