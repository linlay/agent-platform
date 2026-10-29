package server

import runtimeproxy "agent-platform/internal/runtime/proxy"

type proxyRunRoute = runtimeproxy.Route

func (s *Server) lookupProxyRun(runID string) (*proxyRunRoute, bool) {
	if s == nil || s.proxyRuntime == nil {
		return nil, false
	}
	return s.proxyRuntime.Lookup(runID)
}
