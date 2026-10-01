// Package session freezes an Agent's execution context, tools, paths, skills,
// connector grants and history before an executor is started.
package session

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	runtimetypes "agent-platform/internal/runtime/types"
)

// Catalog exposes only the definition and catalog snapshots used by a session.
type Catalog interface {
	AgentDefinition(string) (catalog.AgentDefinition, bool)
	AgentDigests() []contracts.AgentDigest
	SkillIDs() []string
	SkillDefinition(string) (catalog.SkillDefinition, bool)
}

type ProfileBuilder interface {
	Profiles(runtimetypes.QueryCommand, contracts.QuerySession) ([]contracts.SystemInitProfile, error)
}

type Dependencies struct {
	Profiles ProfileBuilder
	Config   config.Config
	Chats    chat.Store
	Registry Catalog
	Models   *models.ModelRegistry
	Runs     contracts.RunManager
	Tools    contracts.ToolExecutor
}

type Builder struct{ deps Dependencies }

func New(deps Dependencies) *Builder { return &Builder{deps: deps} }
