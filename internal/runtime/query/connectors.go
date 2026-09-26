package query

import (
	"agent-platform/internal/connector"
)

func (s *Service) connectorSources() connector.Sources {
	return s.deps.Config.Paths.ConnectorSources()
}
