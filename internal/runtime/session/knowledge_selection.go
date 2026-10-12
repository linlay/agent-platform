package session

import (
	"encoding/json"
	"fmt"
	"slices"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/knowledge/libraryinfo"
	runtimetypes "agent-platform/internal/runtime/types"
)

const MaxLibraryReferences = 8

func NormalizeReferencedKbases(ids []string) []string {
	return contracts.NormalizeRequiredLibraries(ids)
}

// The directory and admission share the existing native connector mount gate.
// Tool exclusions remain the router's responsibility; selection grants no tools.
func KBaseCapability(def catalog.AgentDefinition) libraryinfo.Capability {
	if def.Engine == catalog.AgentEngineACP || IsProxyRoutedAgent(def) || catalog.AgentIsChannelMode(def.Mode) {
		return libraryinfo.Capability{Reason: "kbase references are unsupported for this agent"}
	}
	if !slices.Contains(def.Connectors, connector.KBaseControlConnectorID) {
		return libraryinfo.Capability{Reason: "mount builtin.kbase-control to select knowledge libraries"}
	}
	return libraryinfo.Capability{Available: true}
}

func (s *Builder) ResolveKbases(def catalog.AgentDefinition, ids []string) ([]libraryinfo.LibraryOption, error) {
	ids = NormalizeReferencedKbases(ids)
	if len(ids) == 0 {
		return []libraryinfo.LibraryOption{}, nil
	}
	fail := func(message string) ([]libraryinfo.LibraryOption, error) {
		return nil, &runtimetypes.RequestError{Status: 400, Code: "kbase_selection_unavailable", Message: message}
	}
	if !def.Interaction().Attachment.Kbases {
		return fail("interactionConfig.attachment.kbases is disabled")
	}
	if capability := KBaseCapability(def); !capability.Available {
		return fail(capability.Reason)
	}
	if len(ids) > MaxLibraryReferences {
		return fail(fmt.Sprintf("kbase references accept at most %d libraries", MaxLibraryReferences))
	}
	if s.deps.KnowledgeLibrary == nil {
		return fail("knowledge library service unavailable")
	}
	libraries := make([]libraryinfo.LibraryOption, 0, len(ids))
	for _, id := range ids {
		library, err := s.deps.KnowledgeLibrary(id)
		if err != nil {
			return fail(fmt.Sprintf("knowledge library %q: %s", id, err))
		}
		if !library.Readable {
			return fail(fmt.Sprintf("knowledge library %q is unreadable: %s", id, library.UnavailableReason))
		}
		library.Collections = append([]libraryinfo.CollectionSummary{}, library.Collections...)
		libraries = append(libraries, library)
	}
	return libraries, nil
}

func knowledgeSelectionPrompt(boundID string, libraries []libraryinfo.LibraryOption) string {
	if boundID == "" {
		return ""
	}
	metadata, _ := json.Marshal(map[string]string{"boundLibraryId": boundID})
	// Data only: the rules for using these libraries live in the kbase-control skill.
	return "Knowledge library context:\n" + string(metadata)
}
