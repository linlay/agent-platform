// Package webappbridge owns WebApp SDK authorization and service adaptation.
// It never authenticates an application as a Desktop device or an owner JWT.
package webappbridge

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/connectorops"
	"agent-platform/internal/webapp"
)

//go:embed sdk/bridge.mjs
var module []byte

type Handler func(context.Context, *webapp.Scope, json.RawMessage) (any, error)
type Options struct {
	Apps       *webapp.Service
	Owner      string
	Connectors *connectorops.Service
	Actions    map[string]Handler
}
type Service struct {
	apps         *webapp.Service
	owner        string
	connectors   *connectorops.Service
	actions      map[string]Handler
	mu           sync.Mutex
	credentials  *credentials
	internal     *http.Server
	closed       bool
	ctx          context.Context
	cancel       context.CancelFunc
	permits      chan struct{}
	internalHost string
}

func New(options Options) *Service {
	actions := map[string]Handler{}
	for key, value := range options.Actions {
		actions[key] = value
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{ctx: ctx, cancel: cancel, permits: make(chan struct{}, 32), apps: options.Apps, owner: options.Owner, connectors: options.Connectors, actions: actions, credentials: newCredentials()}
}
func SDKModule() []byte                      { return module }
func (s *Service) Module() []byte            { return SDKModule() }
func sdkError(code string, status int) error { return &webapp.Error{Code: code, Status: status} }
func errorCode(err error) (string, int) {
	var op *connectorops.Error
	if errors.As(err, &op) {
		return op.Code, op.Status
	}
	if errors.Is(err, connectorops.ErrDenied) {
		return "app_grant_required", 403
	}
	return webapp.ErrorCode(err)
}
func response(w http.ResponseWriter, action string, result any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	status := 200
	value := map[string]any{"ok": true, "action": action, "result": result}
	if err != nil {
		code, n := errorCode(err)
		status = n
		value = map[string]any{"ok": false, "action": action, "error": map[string]string{"code": code, "message": code}}
	}
	raw, e := json.Marshal(value)
	if e != nil || len(raw) > (1<<20)+4096 {
		status = 413
		raw = []byte(`{"ok":false,"error":{"code":"result_too_large","message":"result_too_large"}}`)
	}
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request, scope *webapp.Scope) {
	s.serve(w, r, scope, false)
}
func (s *Service) serve(w http.ResponseWriter, r *http.Request, scope *webapp.Scope, backend bool) {
	select {
	case s.permits <- struct{}{}:
		defer func() { <-s.permits }()
	default:
		response(w, "", nil, sdkError("app_busy", 429))
		return
	}
	if s.ctx.Err() != nil {
		response(w, "", nil, sdkError("webapp_bridge_unavailable", 503))
		return
	}
	if r.Method != http.MethodPost {
		response(w, "", nil, sdkError("method_not_allowed", 405))
		return
	}
	if r.URL.RawQuery != "" {
		response(w, "", nil, sdkError("invalid_args", 400))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	if backend {
		ctx = context.WithValue(ctx, backendKey{}, true)
	}
	defer cancel()
	stopScope := context.AfterFunc(scope.Context, cancel)
	defer stopScope()
	stopService := context.AfterFunc(s.ctx, cancel)
	defer stopService()
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(finished); _ = http.NewResponseController(w).SetReadDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-finished
		}
	}()
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		response(w, "", nil, sdkError("invalid_args", 415))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		response(w, "", nil, sdkError("invalid_args", 400))
		return
	}
	var request struct {
		Action string          `json:"action"`
		Args   json.RawMessage `json:"args"`
	}
	if webapp.DecodeRequest(raw, &request) != nil || request.Action == "" || len(request.Action) > 128 {
		response(w, "", nil, sdkError("invalid_args", 400))
		return
	}
	if len(request.Args) == 0 {
		request.Args = json.RawMessage(`{}`)
	}

	if scope.Check() != nil {
		response(w, request.Action, nil, sdkError("app_grant_required", 403))
		return
	}
	result, err := s.call(ctx, scope, request.Action, request.Args)
	if s.ctx.Err() != nil || scope.Check() != nil {
		result = nil
		err = sdkError("app_grant_required", 403)
	}
	response(w, request.Action, result, err)
}
func decode(raw json.RawMessage, out any) error {
	if webapp.DecodeRequest(raw, out) != nil {
		return sdkError("invalid_args", 400)
	}
	return nil
}
func (s *Service) connectorScope(scope *webapp.Scope) connectorops.Scope {
	permissions := make([]connectorops.Permission, 0, len(scope.Policy.Connectors))
	for _, permission := range scope.Policy.Connectors {
		permissions = append(permissions, connectorops.Permission{ConnectorID: permission.ConnectorID, Adapter: permission.Adapter})
	}
	return connectorops.Scope{Subject: s.owner, IdempotencyNamespace: "webapp:" + scope.InstallationID, Execution: permissions, Check: scope.Check, Audit: map[string]any{"source": "webapp", "webappId": scope.Entry.ID, "installationId": scope.InstallationID, "generation": scope.Generation}}
}
func (s *Service) call(ctx context.Context, scope *webapp.Scope, action string, args json.RawMessage) (any, error) {
	switch action {
	case "desktop.capabilities.list":
		var empty struct{}
		if err := decode(args, &empty); err != nil {
			return nil, err
		}
		return s.capabilities(scope), nil
	case "desktop.app.getConfig":
		var empty struct{}
		if err := decode(args, &empty); err != nil {
			return nil, err
		}
		return scope.Manifest.AppConfig, nil
	case "desktop.app.getUserConfig":
		var empty struct{}
		if err := decode(args, &empty); err != nil {
			return nil, err
		}
		return scope.UserConfig, nil
	case "desktop.requestAccess":
		var input struct {
			Capability  string `json:"capability"`
			ConnectorID string `json:"connectorId"`
			Adapter     string `json:"adapter"`
		}
		if err := decode(args, &input); err != nil {
			return nil, err
		}
		if input.Capability != "connector.execute" {
			return nil, sdkError("not_supported", 422)
		}
		for _, permission := range scope.Policy.Connectors {
			if permission.ConnectorID == input.ConnectorID && permission.Adapter == input.Adapter {
				return map[string]string{"status": "granted"}, nil
			}
		}
		return nil, sdkError("app_grant_required", 403)
	case "connector.list":
		var empty struct{}
		if err := decode(args, &empty); err != nil {
			return nil, err
		}
		items := []map[string]any{}
		if s.connectors == nil {
			return nil, sdkError("connector_unavailable", 503)
		}
		auth := s.connectorScope(scope)
		ids := []string{}
		seen := map[string]bool{}
		for _, permission := range scope.Policy.Connectors {
			if !seen[permission.ConnectorID] {
				seen[permission.ConnectorID] = true
				ids = append(ids, permission.ConnectorID)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			catalog, err := s.connectors.Describe(auth, id)
			if err != nil {
				continue
			}
			pkg, err := s.connectors.Sources.Load(id)
			if err != nil {
				continue
			}
			items = append(items, map[string]any{"connectorId": id, "name": pkg.Manifest.Localized("en").Name, "packageVersion": pkg.Version, "adapters": catalog.Adapters})
		}
		return map[string]any{"items": items}, nil
	case "connector.describe":
		var input struct {
			ConnectorID string `json:"connectorId"`
		}
		if err := decode(args, &input); err != nil {
			return nil, err
		}
		if s.connectors == nil {
			return nil, sdkError("connector_unavailable", 503)
		}
		return s.connectors.Describe(s.connectorScope(scope), input.ConnectorID)
	case "connector.invoke":
		var input connectorops.Request
		if err := decode(args, &input); err != nil {
			return nil, err
		}
		if s.connectors == nil {
			return nil, sdkError("connector_unavailable", 503)
		}
		if input.IdempotencyKey == "" {
			return nil, sdkError("idempotency_key_required", 400)
		}
		return s.connectors.Invoke(ctx, s.connectorScope(scope), input)
	}
	if handler := s.actions[action]; handler != nil {
		if strings.HasPrefix(action, "assistant.") && !scope.Policy.Assistant || strings.HasPrefix(action, "artifact.") && !scope.Policy.Artifacts || strings.HasPrefix(action, "skill.") && len(scope.Policy.Skills) == 0 {
			return nil, sdkError("app_grant_required", 403)
		}
		return handler(ctx, scope, args)
	}
	return nil, sdkError("not_supported", 422)
}
func (s *Service) capabilities(scope *webapp.Scope) any {
	states := []map[string]any{}
	all := strings.Fields("auth.session assistant.chat assistant.execute assistant.image connector.execute skill.read artifact.read kanban.read artifact.present desktop.permissions desktop.theme desktop.locale desktop.connector.authenticate desktop.browser.external desktop.dialog.files desktop.dialog.directories desktop.dialog.savePath desktop.microphone desktop.clipboard.write desktop.notification automation.manage desktop.screen.capture desktop.clipboard.read desktop.file.reveal desktop.window desktop.camera desktop.share")
	for _, id := range all {
		supported := false
		granted := false
		switch id {
		case "connector.execute":
			supported = s.connectors != nil
			granted = len(scope.Policy.Connectors) > 0
		case "desktop.permissions":
			supported = true
			granted = true
		case "assistant.chat":
			supported = s.actions["assistant.chat"] != nil
			granted = scope.Policy.Assistant
		case "assistant.execute":
			supported = s.actions["assistant.execute"] != nil
			granted = scope.Policy.Assistant
		case "skill.read":
			supported = s.actions["skill.list"] != nil
			granted = len(scope.Policy.Skills) > 0
		case "artifact.read":
			supported = s.actions["artifact.list"] != nil
			granted = scope.Policy.Artifacts
		}
		status := "unavailable"
		permission := "unavailable"
		if supported {
			status = "available"
			permission = "denied"
			if granted {
				permission = "granted"
			}
		}
		states = append(states, map[string]any{"id": id, "status": status, "declared": granted, "permission": permission})
	}
	return map[string]any{"bridgeVersion": 2, "host": "compose", "capabilities": states}
}

type backendKey struct{}
