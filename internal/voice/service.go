package voice

import (
	"context"
	"errors"
	"sync"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	rt "agent-platform/internal/runtime/types"
	"agent-platform/internal/speech"
	"github.com/gorilla/websocket"
)

type Runtime interface {
	ExecuteQueryWithHooks(context.Context, rt.QueryCommand, rt.QueryHooks) (rt.QueryResult, error)
	AttachRun(context.Context, rt.RunRef, int64) (*rt.Subscription, error)
	Interrupt(context.Context, rt.InterruptCommand) (rt.InterruptResult, error)
}

type ChatReader interface {
	Summary(string) (*chat.Summary, error)
	PrimaryChatID(string) (string, error)
}

type Dependencies struct {
	Config  config.VoiceConfig
	Models  *models.ModelRegistry
	Runtime Runtime
	Chats   ChatReader
	Agents  interface {
		AgentDefinition(string) (catalog.AgentDefinition, bool)
	}
	Runs interface {
		ActiveRunForChat(string) (contracts.RunStatusInfo, bool, error)
	}
}

type Service struct {
	deps    Dependencies
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closing bool
	wg      sync.WaitGroup
}

func NewService(ctx context.Context, deps Dependencies) *Service {
	ctx, cancel := context.WithCancel(ctx)
	return &Service{deps: deps, ctx: ctx, cancel: cancel}
}

// start/Close only account for this service's goroutines, not Run state.
func (s *Service) start(work func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.ctx.Err() != nil {
		return false
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); work() }()
	return true
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closing = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) readiness() string {
	if s == nil || !s.deps.Config.Enabled {
		return "voice_disabled"
	}
	if s.ctx.Err() != nil || s.deps.Runtime == nil || s.deps.Chats == nil || s.deps.Agents == nil || s.deps.Runs == nil {
		return "voice_unavailable"
	}
	a, ap, t, tp, err := s.speechModels()
	if err == nil {
		_, err = speech.NewASRClient(a, ap, s.deps.Config.MaxSentenceSilenceMS)
	}
	if err == nil {
		_, err = speech.NewTTSClient(t, tp)
	}
	if err != nil {
		return "voice_models_unavailable"
	}
	return ""
}

func (s *Service) speechModels() (models.ModelDefinition, models.ProviderDefinition, models.ModelDefinition, models.ProviderDefinition, error) {
	var a, t models.ModelDefinition
	var ap, tp models.ProviderDefinition
	if s.deps.Models == nil {
		return a, ap, t, tp, errors.New("voice models unavailable")
	}
	a, ap, err := s.deps.Models.GetTyped(s.deps.Config.ASRModel, models.ModelTypeASR)
	if err == nil {
		t, tp, err = s.deps.Models.GetTyped(s.deps.Config.TTSModel, models.ModelTypeTTS)
	}
	if err == nil && (a.Protocol != models.ProtocolDashScopeASR || t.Protocol != models.ProtocolDashScopeRealtimeTTS || !ap.HasCredential() || !tp.HasCredential()) {
		err = errors.New("voice model capability unavailable")
	}
	return a, ap, t, tp, err
}

func (s *Service) Capabilities() map[string]any {
	reason := s.readiness()
	voices := []map[string]string{}
	defaultVoice := ""
	if s != nil {
		defaultVoice = s.deps.Config.DefaultVoice
		for _, id := range s.deps.Config.Voices {
			voices = append(voices, map[string]string{"id": id, "label": id})
		}
	}
	return map[string]any{"enabled": reason == "", "reason": reason, "version": 1, "websocketPath": "/api/voice/ws",
		"input":  map[string]any{"codecs": []string{"pcm_s16le"}, "sampleRate": 16000, "channels": 1},
		"output": map[string]any{"codecs": []string{"pcm_s16le"}, "sampleRates": []int{16000, 24000}},
		"voices": voices, "defaultVoice": defaultVoice}
}

type Caller struct {
	Identity        *contracts.AuthIdentity
	Locale          string
	ResourceBaseURL string
}

func (s *Service) Serve(conn *websocket.Conn, caller Caller) {
	if s == nil || s.readiness() != "" {
		_ = conn.Close()
		return
	}
	// Reserve before Close can begin waiting; Serve itself stays synchronous for HTTP.
	s.mu.Lock()
	if s.closing || s.ctx.Err() != nil {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	runSession(s, conn, caller)
}
