// Package fg implements the "fg" run mode: a long-lived Huawei FunctionGraph
// handler driven by timer triggers. Each invocation carries the desired state in
// the timer's user_event field; the handler applies one scaling action.
package fg

import (
	"context"
	"encoding/json"
	"fmt"

	fgcontext "github.com/rahadiangg/huaweicloud-go-runtime/go-api/context"
	"github.com/rahadiangg/huaweicloud-go-runtime/events/timer"
	"github.com/rahadiangg/huaweicloud-go-runtime/pkg/runtime"

	"github.com/rahadiangg/siesta/internal/config"
	"github.com/rahadiangg/siesta/internal/scaler"
)

// newScaler is the provider constructor; overridable in tests.
var newScaler = scaler.New

// Serve registers the FunctionGraph handler and blocks serving invocations.
// It never returns under normal operation.
func Serve(getenv config.Getenv) {
	runtime.Register(makeHandler(getenv))
}

// makeHandler returns the FG handler closure bound to environment defaults. It
// extracts agency credentials and the logger from the runtime context, then
// delegates to the pure handle core.
func makeHandler(getenv config.Getenv) func([]byte, fgcontext.RuntimeContext) (any, error) {
	return func(payload []byte, ctx fgcontext.RuntimeContext) (any, error) {
		log := ctx.GetLogger().Logf
		res, err := handle(context.Background(), payload, getenv, ctxCreds(ctx))
		if err != nil {
			log("siesta: error: %v", err)
			// Must return a non-nil value: the runtime treats nil as the error
			// "response is empty".
			return map[string]string{"status": "error", "error": err.Error()}, err
		}
		log("siesta: provider=%s nodePool=%s previous=%d desired=%d changed=%v dryRun=%v",
			res.Provider, res.NodePoolID, res.Previous, res.Desired, res.Changed, res.DryRun)
		return res, nil
	}
}

// ctxCreds extracts credentials from the FunctionGraph request context. When an
// agency is configured, FG provides temporary credentials as a matched triple via
// the "security" getters (the preferred, non-deprecated path). The legacy
// GetAccessKey/GetSecretKey getters are a fallback and carry no token, so they
// must not be mixed with the security token.
func ctxCreds(ctx fgcontext.RuntimeContext) scaler.Creds {
	ak, sk, token := ctx.GetSecurityAccessKey(), ctx.GetSecuritySecretKey(), ctx.GetSecurityToken()
	if ak == "" {
		ak, sk, token = ctx.GetAccessKey(), ctx.GetSecretKey(), ""
	}
	return scaler.Creds{AK: ak, SK: sk, ProjectID: ctx.GetProjectID(), SecurityToken: token}
}

// handle is the pure, testable core: parse the timer event, build the request,
// resolve credentials (context first, env fallback), and scale.
func handle(ctx context.Context, payload []byte, getenv config.Getenv, ctxCreds scaler.Creds) (scaler.Result, error) {
	settings := config.SettingsFromEnv(getenv)

	var ev timer.TimerTriggerEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return scaler.Result{}, fmt.Errorf("parse timer event: %w", err)
	}

	ue, err := config.ParseUserEvent(ev.UserEvent)
	if err != nil {
		return scaler.Result{}, err
	}

	req := ue.Request(settings)
	if err := req.Validate(); err != nil {
		return scaler.Result{}, err
	}

	creds := config.MergeCreds(ctxCreds, config.CredsFromEnv(getenv))
	s, err := newScaler(settings.Provider, creds, settings.Options())
	if err != nil {
		return scaler.Result{}, err
	}
	return s.Scale(ctx, req)
}
