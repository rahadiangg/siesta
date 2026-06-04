package fg

import (
	"context"
	"errors"
	"testing"

	fgcommon "github.com/rahadiangg/huaweicloud-go-runtime/pkg/runtime/common"

	"github.com/rahadiangg/siesta/internal/config"
	"github.com/rahadiangg/siesta/internal/scaler"
)

// --- fakes ---

type fakeScaler struct {
	res    scaler.Result
	err    error
	gotReq scaler.Request
	called bool
}

func (f *fakeScaler) Scale(_ context.Context, r scaler.Request) (scaler.Result, error) {
	f.called = true
	f.gotReq = r
	return f.res, f.err
}

type fakeLogger struct{}

func (fakeLogger) Logf(string, ...any) {}

// fakeCtx implements fgcontext.RuntimeContext with the credential getters set.
// sak/ssk/token model the agency "security" credentials; ak/sk model the legacy
// (deprecated) credentials.
type fakeCtx struct {
	ak, sk, projectID string
	sak, ssk, token   string
}

func (c fakeCtx) GetRequestID() string                { return "req-1" }
func (c fakeCtx) GetRemainingTimeInMilliSeconds() int { return 1000 }
func (c fakeCtx) GetAccessKey() string                { return c.ak }
func (c fakeCtx) GetSecretKey() string                { return c.sk }
func (c fakeCtx) GetSecurityAccessKey() string        { return c.sak }
func (c fakeCtx) GetSecuritySecretKey() string        { return c.ssk }
func (c fakeCtx) GetFunctionName() string             { return "siesta" }
func (c fakeCtx) GetUserData(string) string           { return "" }
func (c fakeCtx) GetLogger() fgcommon.RuntimeLogger   { return fakeLogger{} }
func (c fakeCtx) GetRunningTimeInSeconds() int        { return 0 }
func (c fakeCtx) GetVersion() string                  { return "latest" }
func (c fakeCtx) GetMemorySize() int                  { return 128 }
func (c fakeCtx) GetCPUNumber() int                   { return 1 }
func (c fakeCtx) GetProjectID() string                { return c.projectID }
func (c fakeCtx) GetPackage() string                  { return "default" }
func (c fakeCtx) GetToken() string                    { return "" }
func (c fakeCtx) GetSecurityToken() string            { return c.token }

func withScaler(t *testing.T, f func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error)) {
	t.Helper()
	old := newScaler
	newScaler = f
	t.Cleanup(func() { newScaler = old })
}

func baseEnv() config.Getenv {
	return config.MapGetenv(map[string]string{
		"SIESTA_CLUSTER":  "c1",
		"SIESTA_NODEPOOL": "prod",
		"SIESTA_REGION":   "cn-north-4",
	})
}

// timerPayload builds the JSON the runtime would deliver, with user_event nested.
func timerPayload(userEvent string) []byte {
	// TimerTriggerEvent uses json tag "user_event".
	return []byte(`{"version":"v1.0","trigger_name":"t","trigger_type":"TIMER","user_event":` + jsonString(userEvent) + `}`)
}

// jsonString quotes s as a JSON string literal.
func jsonString(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		default:
			out = append(out, byte(r))
		}
	}
	return string(append(out, '"'))
}

// --- tests ---

func TestHandle_Valid(t *testing.T) {
	fs := &fakeScaler{res: scaler.Result{Provider: "huawei-cce", NodePoolID: "np-1", Desired: 3, Changed: true}}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	res, err := handle(context.Background(), timerPayload(`{"count":3}`), baseEnv(), scaler.Creds{})
	if err != nil {
		t.Fatalf("handle error: %v", err)
	}
	if !fs.called || fs.gotReq.Desired != 3 || fs.gotReq.Cluster != "c1" || fs.gotReq.NodePoolName != "prod" {
		t.Fatalf("unexpected request: %+v", fs.gotReq)
	}
	if res.Desired != 3 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestHandle_EventOverridesBase(t *testing.T) {
	fs := &fakeScaler{}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	_, err := handle(context.Background(), timerPayload(`{"cluster":"c2","nodePool":"batch","count":0}`), baseEnv(), scaler.Creds{})
	if err != nil {
		t.Fatalf("handle error: %v", err)
	}
	if fs.gotReq.Cluster != "c2" || fs.gotReq.NodePoolName != "batch" || fs.gotReq.Desired != 0 {
		t.Fatalf("event should override base: %+v", fs.gotReq)
	}
}

func TestHandle_MalformedPayload(t *testing.T) {
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return &fakeScaler{}, nil })
	_, err := handle(context.Background(), []byte(`{not json`), baseEnv(), scaler.Creds{})
	if err == nil {
		t.Fatal("expected error for malformed payload")
	}
}

func TestHandle_EmptyUserEvent(t *testing.T) {
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return &fakeScaler{}, nil })
	_, err := handle(context.Background(), timerPayload(``), baseEnv(), scaler.Creds{})
	if err == nil {
		t.Fatal("expected error for empty user_event")
	}
}

func TestHandle_InvalidRequest(t *testing.T) {
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return &fakeScaler{}, nil })
	// No cluster/nodepool in env or event → request validation fails.
	_, err := handle(context.Background(), timerPayload(`{"count":1}`), config.MapGetenv(nil), scaler.Creds{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestHandle_BuildError(t *testing.T) {
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) {
		return nil, errors.New("bad creds")
	})
	_, err := handle(context.Background(), timerPayload(`{"count":1}`), baseEnv(), scaler.Creds{})
	if err == nil {
		t.Fatal("expected build error")
	}
}

func TestHandle_CredsPreferContextThenEnv(t *testing.T) {
	var gotCreds scaler.Creds
	withScaler(t, func(_ string, c scaler.Creds, _ scaler.Options) (scaler.Scaler, error) {
		gotCreds = c
		return &fakeScaler{}, nil
	})

	getenv := config.MapGetenv(map[string]string{
		"SIESTA_CLUSTER":         "c1",
		"SIESTA_NODEPOOL":        "prod",
		"SIESTA_REGION":          "cn-north-4",
		"HUAWEICLOUD_SDK_AK":     "env-ak",
		"HUAWEICLOUD_SDK_SK":     "env-sk",
		"HUAWEICLOUD_PROJECT_ID": "env-proj",
	})
	// Context provides AK/SK/token but no project ID → project ID falls back to env.
	ctxCreds := scaler.Creds{AK: "ctx-ak", SK: "ctx-sk", SecurityToken: "ctx-tok"}

	if _, err := handle(context.Background(), timerPayload(`{"count":1}`), getenv, ctxCreds); err != nil {
		t.Fatalf("handle error: %v", err)
	}
	if gotCreds.AK != "ctx-ak" || gotCreds.SK != "ctx-sk" || gotCreds.SecurityToken != "ctx-tok" {
		t.Fatalf("context creds should win: %+v", gotCreds)
	}
	if gotCreds.ProjectID != "env-proj" {
		t.Fatalf("project ID should fall back to env: %+v", gotCreds)
	}
}

func TestMakeHandler_SuccessReturnsNonNil(t *testing.T) {
	fs := &fakeScaler{res: scaler.Result{Provider: "huawei-cce", Desired: 3, Changed: true}}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	h := makeHandler(baseEnv())
	val, err := h(timerPayload(`{"count":3}`), fakeCtx{ak: "a", sk: "b", projectID: "p"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if val == nil {
		t.Fatal("handler must return non-nil value (FG 'response is empty' trap)")
	}
}

func TestMakeHandler_ErrorReturnsNonNil(t *testing.T) {
	fs := &fakeScaler{err: errors.New("boom")}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	h := makeHandler(baseEnv())
	val, err := h(timerPayload(`{"count":3}`), fakeCtx{ak: "a", sk: "b", projectID: "p"})
	if err == nil {
		t.Fatal("expected error from handler")
	}
	if val == nil {
		t.Fatal("handler must return non-nil value even on error")
	}
}

func TestMakeHandler_ExtractsAgencyCreds(t *testing.T) {
	// Agency creds: the security triple wins, with its token. Legacy ak/sk are
	// present but must be ignored (mixing them with the token causes 401s).
	var gotCreds scaler.Creds
	withScaler(t, func(_ string, c scaler.Creds, _ scaler.Options) (scaler.Scaler, error) {
		gotCreds = c
		return &fakeScaler{}, nil
	})

	h := makeHandler(baseEnv())
	_, err := h(timerPayload(`{"count":1}`), fakeCtx{
		ak: "legacy-ak", sk: "legacy-sk", projectID: "ctx-proj",
		sak: "sec-ak", ssk: "sec-sk", token: "sec-tok",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if gotCreds.AK != "sec-ak" || gotCreds.SK != "sec-sk" || gotCreds.ProjectID != "ctx-proj" || gotCreds.SecurityToken != "sec-tok" {
		t.Fatalf("handler should use security (agency) creds: %+v", gotCreds)
	}
}

func TestCtxCreds_LegacyFallback(t *testing.T) {
	// No security creds → fall back to legacy ak/sk with no token.
	got := ctxCreds(fakeCtx{ak: "legacy-ak", sk: "legacy-sk", projectID: "p"})
	if got.AK != "legacy-ak" || got.SK != "legacy-sk" || got.ProjectID != "p" || got.SecurityToken != "" {
		t.Fatalf("legacy fallback wrong: %+v", got)
	}
}

func TestHandle_ScaleGroupFromUserEvent(t *testing.T) {
	fs := &fakeScaler{res: scaler.Result{Provider: "huawei-cce", ScaleGroup: "g-az3", Changed: true}}
	withScaler(t, func(string, scaler.Creds, scaler.Options) (scaler.Scaler, error) { return fs, nil })

	_, err := handle(context.Background(),
		timerPayload(`{"az":"ap-southeast-4c","flavor":"s7n.xlarge.2","scalePolicy":"azbalance","count":1}`),
		baseEnv(), scaler.Creds{})
	if err != nil {
		t.Fatalf("handle error: %v", err)
	}
	if fs.gotReq.AZ != "ap-southeast-4c" || fs.gotReq.Flavor != "s7n.xlarge.2" || fs.gotReq.ScalePolicy != "azbalance" || fs.gotReq.Desired != 1 {
		t.Fatalf("scale-group fields not passed through: %+v", fs.gotReq)
	}
	if !fs.gotReq.TargetsGroup() {
		t.Fatal("expected TargetsGroup() true")
	}
}
