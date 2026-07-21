package kbase

type stubAgentSource struct {
	agents map[string]AgentSpec
}

func (s stubAgentSource) Agents() []AgentSpec {
	out := make([]AgentSpec, 0, len(s.agents))
	for _, agent := range s.agents {
		out = append(out, agent)
	}
	return out
}

func (s stubAgentSource) Agent(key string) (AgentSpec, bool) {
	agent, ok := s.agents[key]
	return agent, ok
}

func testKBaseAgent(key, workspace, storage string) AgentSpec {
	return AgentSpec{
		Key: key, Mode: Mode, WorkspaceRoot: workspace,
		Config: AgentConfig{
			Embedding: EmbeddingConfig{ModelKey: "mock-embedding-key"},
			Storage:   StorageConfig{Location: storage},
			Include:   []string{"**/*.md", "**/*.txt"},
			Exclude:   []string{".git/**", ".kbase/**", "node_modules/**"},
			Chunk:     ChunkConfig{Unit: ChunkUnitEstimatedTokens, MaxTokens: 1000, OverlapTokens: 100},
			Retrieval: RetrievalConfig{TopK: 5, VectorWeight: 0.7, FTSWeight: 0.3},
		},
	}
}
