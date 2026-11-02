package voice

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"agent-platform/internal/chat"
	"github.com/google/uuid"
)

type audioFormat struct {
	Codec      string `json:"codec"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels,omitempty"`
	FrameMS    int    `json:"frameMs,omitempty"`
}

type clientMessage struct {
	Type     string `json:"type"`
	Version  int    `json:"version,omitempty"`
	AgentKey string `json:"agentKey,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
	Voice    string `json:"voice,omitempty"`
	Audio    struct {
		Input  audioFormat `json:"input"`
		Output audioFormat `json:"output"`
	} `json:"audio,omitempty"`
	State  string `json:"state,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func decodeMessage(data []byte) (clientMessage, error) {
	var msg clientMessage
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&msg); err != nil {
		return msg, errors.New("invalid voice message")
	}
	if d.Decode(new(any)) != io.EOF {
		return msg, errors.New("invalid voice message")
	}
	return msg, nil
}

func (s *Service) resolveHello(msg clientMessage) (string, bool, string, error) {
	if msg.Type != "hello" || msg.Version != 1 || msg.AgentKey == "" {
		return "", false, "", errors.New("invalid voice hello")
	}
	if _, ok := s.deps.Agents.AgentDefinition(msg.AgentKey); !ok {
		return "", false, "", errors.New("agent unavailable")
	}
	in, out := msg.Audio.Input, msg.Audio.Output
	if in.Codec != "pcm_s16le" || in.SampleRate != 16000 || in.Channels != 1 || in.FrameMS != 20 || out.Codec != "pcm_s16le" || (out.SampleRate != 16000 && out.SampleRate != 24000) || (out.Channels != 0 && out.Channels != 1) || (out.FrameMS != 0 && out.FrameMS != 20) {
		return "", false, "", errors.New("unsupported voice audio format")
	}
	voice := msg.Voice
	if voice == "" {
		voice = s.deps.Config.DefaultVoice
	}
	allowed := false
	for _, id := range s.deps.Config.Voices {
		allowed = allowed || id == voice
	}
	if !allowed {
		return "", false, "", errors.New("voice not allowed")
	}
	chatID := msg.ChatID
	var err error
	if chatID == "" {
		chatID, err = s.deps.Chats.PrimaryChatID(msg.AgentKey)
	}
	if err != nil {
		return "", false, "", errors.New("chat unavailable")
	}
	if chatID == "" {
		return uuid.NewString(), true, voice, nil
	}
	if !chat.ValidChatID(chatID) {
		return "", false, "", errors.New("invalid chatId")
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil || summary == nil || summary.AgentKey != msg.AgentKey || chat.IsPendingUploadChat(*summary) {
		return "", false, "", errors.New("chat does not belong to agent")
	}
	return chatID, false, voice, nil
}
