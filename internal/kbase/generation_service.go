package kbase

type generationService struct {
	index       IndexOptions
	maintenance MaintenanceOptions
	resolver    *capabilityResolver
	runtime     *lanceRuntime
}

func newGenerationService(index IndexOptions, maintenance MaintenanceOptions, resolver *capabilityResolver, runtime *lanceRuntime) *generationService {
	return &generationService{index: index, maintenance: maintenance, resolver: resolver, runtime: runtime}
}
