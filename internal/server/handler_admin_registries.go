package server

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

const (
	adminRegistryStatusReady    = "ready"
	adminRegistryStatusInvalid  = "invalid"
	adminRegistryStatusDisabled = "disabled"
)

var adminRegistryCategories = []string{"providers", "models", "mcp-servers", "viewport-servers"}

func (s *Server) handleAdminRegistries(w http.ResponseWriter, r *http.Request) {
	response, err := s.listAdminRegistries()
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminRegistryDetail(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		response, err := s.readAdminRegistryDetail(r.URL.Query().Get("category"), r.URL.Query().Get("file"))
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var req api.AdminRegistryDetailRequest
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
			return
		}
		response, err := s.saveAdminRegistryDetail(req)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) handleAdminRegistryValidate(w http.ResponseWriter, r *http.Request) {
	var req api.AdminRegistryValidateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.validateAdminRegistry(req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) listAdminRegistries() (api.AdminRegistryListResponse, error) {
	items := []api.AdminRegistryListItem{}
	for _, category := range adminRegistryCategories {
		dir, err := s.adminRegistryCategoryDir(category)
		if err != nil {
			return api.AdminRegistryListResponse{}, err
		}
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return api.AdminRegistryListResponse{}, err
		}
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name())
			if entry.IsDir() || !isAdminRegistryYAMLFile(name) {
				continue
			}
			path := filepath.Join(dir, name)
			content, readErr := os.ReadFile(path)
			info, statErr := entry.Info()
			if readErr != nil {
				items = append(items, adminRegistryListItem(api.AdminRegistrySummary{
					Category: category,
					File:     name,
					Status:   adminRegistryStatusInvalid,
					Diagnostics: []api.AdminAgentDiagnostic{adminRegistryDiagnostic(
						"error", "read_failed", readErr.Error(), path,
					)},
					Source: adminRegistrySource(category, path),
				}))
				continue
			}
			summary, _, _ := s.analyzeAdminRegistry(category, name, content, path)
			if statErr == nil && info != nil {
				summary.UpdatedAt = info.ModTime().UnixMilli()
				summary.Size = info.Size()
			}
			items = append(items, adminRegistryListItem(s.adminRegistryListSummary(summary)))
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Category != items[j].Category {
			return items[i].Category < items[j].Category
		}
		return items[i].File < items[j].File
	})
	return api.AdminRegistryListResponse{Items: items, Total: len(items)}, nil
}

func (s *Server) adminRegistryListSummary(summary api.AdminRegistrySummary) api.AdminRegistrySummary {
	if summary.Category != "mcp-servers" {
		return summary
	}
	if summary.Summary == nil {
		summary.Summary = map[string]any{}
	}
	summary.Summary["toolCount"] = s.adminRegistryMCPToolCount(summary.Key)
	return summary
}

func (s *Server) adminRegistryMCPToolCount(serverKey string) int {
	serverKey = strings.TrimSpace(serverKey)
	if s == nil || s.deps.Tools == nil || serverKey == "" {
		return 0
	}
	count := 0
	seen := map[string]struct{}{}
	for _, tool := range s.deps.Tools.Definitions() {
		if canonical, ok := canonicalizePublicToolDefinition(tool); ok {
			tool = canonical
		}
		if toolSourceCategory(tool) != "mcp" {
			continue
		}
		sourceKey := strings.TrimSpace(anyStringValue(tool.Meta["sourceKey"]))
		if sourceKey == "" {
			sourceKey = strings.TrimSpace(anyStringValue(tool.Meta["serverKey"]))
		}
		if !strings.EqualFold(sourceKey, serverKey) {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(tool.Name))
		if name == "" {
			name = strings.ToLower(strings.TrimSpace(tool.Key))
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		count++
	}
	return count
}

func adminRegistryListItem(summary api.AdminRegistrySummary) api.AdminRegistryListItem {
	item := api.AdminRegistryListItem{
		Category:  summary.Category,
		File:      summary.File,
		Key:       summary.Key,
		Name:      summary.Name,
		Status:    summary.Status,
		Summary:   summary.Summary,
		UpdatedAt: summary.UpdatedAt,
	}
	if len(summary.Diagnostics) > 0 {
		first := summary.Diagnostics[0]
		item.Diagnostic = &api.AdminRegistryListDiagnostic{
			Severity: first.Severity,
			Code:     first.Code,
			Message:  first.Message,
		}
		item.DiagnosticCount = len(summary.Diagnostics)
	}
	return item
}

func (s *Server) readAdminRegistryDetail(category string, file string) (api.AdminRegistryDetailResponse, error) {
	path, err := s.adminRegistryFilePath(category, file)
	if err != nil {
		return api.AdminRegistryDetailResponse{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return api.AdminRegistryDetailResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "registry file not found")
		}
		return api.AdminRegistryDetailResponse{}, err
	}
	summary, parsed, _ := s.analyzeAdminRegistry(category, file, content, path)
	if info, statErr := os.Stat(path); statErr == nil {
		summary.UpdatedAt = info.ModTime().UnixMilli()
		summary.Size = info.Size()
	}
	return api.AdminRegistryDetailResponse{
		AdminRegistrySummary: summary,
		Content:              string(content),
		Parsed:               parsed,
	}, nil
}

func (s *Server) saveAdminRegistryDetail(req api.AdminRegistryDetailRequest) (api.AdminRegistryDetailResponse, error) {
	path, err := s.adminRegistryFilePath(req.Category, req.File)
	if err != nil {
		return api.AdminRegistryDetailResponse{}, err
	}
	summary, parsed, syntaxOK := s.analyzeAdminRegistry(req.Category, req.File, []byte(req.Content), path)
	if !syntaxOK {
		return api.AdminRegistryDetailResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_yaml", firstDiagnosticMessage(summary.Diagnostics, "invalid yaml"))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return api.AdminRegistryDetailResponse{}, err
	}
	content := []byte(req.Content)
	if len(content) > 0 && !bytes.HasSuffix(content, []byte("\n")) {
		content = append(content, '\n')
	}
	if err := atomicWriteAdminRegistryFile(path, content); err != nil {
		return api.AdminRegistryDetailResponse{}, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		summary.UpdatedAt = info.ModTime().UnixMilli()
		summary.Size = info.Size()
	}
	return api.AdminRegistryDetailResponse{
		AdminRegistrySummary: summary,
		Content:              string(content),
		Parsed:               parsed,
	}, nil
}

func (s *Server) validateAdminRegistry(req api.AdminRegistryValidateRequest) (api.AdminRegistryValidateResponse, error) {
	if _, err := s.adminRegistryCategoryDir(req.Category); err != nil {
		return api.AdminRegistryValidateResponse{}, err
	}
	file := strings.TrimSpace(req.File)
	if file != "" {
		if _, err := s.adminRegistryFilePath(req.Category, file); err != nil {
			return api.AdminRegistryValidateResponse{}, err
		}
	}
	summary, parsed, _ := s.analyzeAdminRegistry(req.Category, file, []byte(req.Content), "")
	return api.AdminRegistryValidateResponse{
		Status:      summary.Status,
		Diagnostics: summary.Diagnostics,
		Summary:     summary.Summary,
		Parsed:      parsed,
	}, nil
}

func (s *Server) analyzeAdminRegistry(category string, file string, content []byte, sourcePath string) (api.AdminRegistrySummary, map[string]any, bool) {
	summary := api.AdminRegistrySummary{
		Category: strings.TrimSpace(category),
		File:     strings.TrimSpace(file),
		Status:   adminRegistryStatusReady,
		Source:   adminRegistrySource(category, sourcePath),
		Summary:  map[string]any{},
	}
	tree, err := config.LoadYAMLTreeBytes(content)
	if err != nil {
		summary.Status = adminRegistryStatusInvalid
		summary.Diagnostics = []api.AdminAgentDiagnostic{adminRegistryDiagnostic("error", "invalid_yaml", err.Error(), sourcePath)}
		return summary, nil, false
	}
	root, ok := tree.(map[string]any)
	if !ok {
		summary.Status = adminRegistryStatusInvalid
		summary.Diagnostics = []api.AdminAgentDiagnostic{adminRegistryDiagnostic("error", "invalid_config", "registry YAML must be a map", sourcePath)}
		return summary, nil, true
	}
	summary.Key = adminRegistryKey(category, file, root)
	summary.Name = strings.TrimSpace(contracts.FirstNonEmptyString(root["name"]))
	summary.Summary = adminRegistryPublicSummary(category, root)
	diagnostics := s.adminRegistryDiagnostics(category, file, root, sourcePath)
	if len(diagnostics) > 0 {
		summary.Diagnostics = diagnostics
		if adminRegistryHasError(diagnostics) {
			summary.Status = adminRegistryStatusInvalid
		}
	}
	if summary.Status == adminRegistryStatusReady && adminRegistryBool(root["enabled"], true) == false {
		summary.Status = adminRegistryStatusDisabled
	}
	return summary, contracts.CloneMap(root), true
}

func (s *Server) adminRegistryDiagnostics(category string, file string, root map[string]any, sourcePath string) []api.AdminAgentDiagnostic {
	diagnostics := []api.AdminAgentDiagnostic{}
	addError := func(code string, message string) {
		diagnostics = append(diagnostics, adminRegistryDiagnostic("error", code, message, sourcePath))
	}
	addWarning := func(code string, message string) {
		diagnostics = append(diagnostics, adminRegistryDiagnostic("warning", code, message, sourcePath))
	}

	switch category {
	case "providers":
		if strings.TrimSpace(contracts.FirstNonEmptyString(root["key"])) == "" {
			addError("missing_key", "provider key is required")
		}
		if strings.TrimSpace(contracts.FirstNonEmptyString(root["baseUrl"], root["base-url"])) == "" {
			addError("missing_base_url", "provider baseUrl is required")
		}
		if strings.TrimSpace(contracts.FirstNonEmptyString(root["apiKey"], root["api-key"])) == "" {
			addWarning("missing_api_key", "provider apiKey is empty")
		}
	case "models":
		if strings.TrimSpace(contracts.FirstNonEmptyString(root["key"])) == "" {
			addError("missing_key", "model key is required")
		}
		modelType, ok := models.NormalizeModelType(contracts.FirstNonEmptyString(root["type"]))
		if !ok {
			addError("invalid_type", "model type must be chat, embedding, or image-generation")
			modelType = models.ModelTypeChat
		}
		protocol := strings.TrimSpace(contracts.FirstNonEmptyString(root["protocol"]))
		if models.IsACPPassthroughProtocol(protocol) && modelType != models.ModelTypeChat {
			addError("invalid_protocol", "ACP_PASSTHROUGH is only supported for type: chat")
		}
		if !models.IsACPPassthroughProtocol(protocol) {
			provider := strings.TrimSpace(contracts.FirstNonEmptyString(root["provider"]))
			if provider == "" {
				addError("missing_provider", "model provider is required")
			} else if !s.adminRegistryProviderExists(provider) {
				addError("unknown_provider", fmt.Sprintf("provider %s not found", provider))
			}
		}
		if strings.TrimSpace(contracts.FirstNonEmptyString(root["modelId"], root["model-id"])) == "" {
			addError("missing_model_id", "modelId is required")
		}
		switch modelType {
		case models.ModelTypeEmbedding:
			embedding := contracts.AnyMapNode(root["embedding"])
			if contracts.AnyIntNode(embedding["dimension"]) <= 0 {
				addError("missing_embedding_dimension", "embedding.dimension must be greater than 0")
			}
		case models.ModelTypeImageGeneration:
			image := contracts.AnyMapNode(root["image"])
			endpoint := strings.TrimSpace(contracts.FirstNonEmptyString(image["endpointPath"], image["endpoint-path"]))
			if endpoint == "" {
				addWarning("missing_image_endpoint", "image.endpointPath is empty; runtime will use the OpenAI-compatible default")
			}
		}
	case "mcp-servers":
		if adminRegistryKey(category, file, root) == "" {
			addError("missing_key", "serverKey or key is required")
		}
		if strings.TrimSpace(contracts.FirstNonEmptyString(root["baseUrl"], root["base-url"], root["url"])) == "" {
			addError("missing_base_url", "MCP server baseUrl is required")
		}
	case "viewport-servers":
		if adminRegistryKey(category, file, root) == "" {
			addError("missing_key", "serverKey or key is required")
		}
		if strings.TrimSpace(contracts.FirstNonEmptyString(root["baseUrl"], root["base-url"], root["url"])) == "" {
			addError("missing_base_url", "viewport server baseUrl is required")
		}
	default:
		addError("invalid_category", "unsupported registry category")
	}
	return diagnostics
}

func (s *Server) adminRegistryProviderExists(provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return false
	}
	dir, err := s.adminRegistryCategoryDir("providers")
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if entry.IsDir() || !isAdminRegistryYAMLFile(name) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		tree, err := config.LoadYAMLTreeBytes(content)
		if err != nil {
			continue
		}
		root, _ := tree.(map[string]any)
		if strings.TrimSpace(contracts.FirstNonEmptyString(root["key"])) == provider {
			return true
		}
	}
	return false
}

func (s *Server) adminRegistryCategoryDir(category string) (string, error) {
	category = strings.TrimSpace(category)
	for _, allowed := range adminRegistryCategories {
		if category == allowed {
			return filepath.Join(s.deps.Config.Paths.RegistriesDir, category), nil
		}
	}
	return "", newAgentStatusError(http.StatusBadRequest, "invalid_request", "unsupported registry category")
}

func (s *Server) adminRegistryFilePath(category string, file string) (string, error) {
	dir, err := s.adminRegistryCategoryDir(category)
	if err != nil {
		return "", err
	}
	file = strings.TrimSpace(file)
	if file == "" {
		return "", newAgentStatusError(http.StatusBadRequest, "invalid_request", "file is required")
	}
	if filepath.IsAbs(file) || file != filepath.Base(file) || strings.Contains(file, "..") || !isAdminRegistryYAMLFile(file) {
		return "", newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid registry file name")
	}
	return filepath.Join(dir, file), nil
}

func isAdminRegistryYAMLFile(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, ".") || catalog.ShouldIgnoreRuntimeWatchPath(name) {
		return false
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")
}

func adminRegistrySource(category string, path string) *api.AgentSource {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return &api.AgentSource{Kind: category, Path: path}
}

func adminRegistryKey(category string, file string, root map[string]any) string {
	switch category {
	case "mcp-servers", "viewport-servers":
		if key := strings.TrimSpace(contracts.FirstNonEmptyString(root["serverKey"], root["server-key"], root["key"])); key != "" {
			return key
		}
	}
	if key := strings.TrimSpace(contracts.FirstNonEmptyString(root["key"])); key != "" {
		return key
	}
	file = strings.TrimSpace(file)
	if file == "" {
		return ""
	}
	ext := filepath.Ext(file)
	return strings.TrimSuffix(file, ext)
}

func adminRegistryPublicSummary(category string, root map[string]any) map[string]any {
	out := map[string]any{}
	put := func(key string, value any) {
		if text, ok := value.(string); ok {
			text = strings.TrimSpace(text)
			if text == "" {
				return
			}
			out[key] = text
			return
		}
		if value != nil {
			out[key] = value
		}
	}
	switch category {
	case "providers":
		put("baseUrl", contracts.FirstNonEmptyString(root["baseUrl"], root["base-url"]))
		put("defaultModel", contracts.FirstNonEmptyString(root["defaultModel"], root["default-model"]))
		if protocols := contracts.AnyMapNode(root["protocols"]); len(protocols) > 0 {
			keys := make([]string, 0, len(protocols))
			for key := range protocols {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			out["protocols"] = keys
		}
	case "models":
		modelType, _ := models.NormalizeModelType(contracts.FirstNonEmptyString(root["type"]))
		put("type", modelType)
		put("provider", contracts.FirstNonEmptyString(root["provider"]))
		put("protocol", contracts.FirstNonEmptyString(root["protocol"]))
		put("modelId", contracts.FirstNonEmptyString(root["modelId"], root["model-id"]))
		put("isVision", root["isVision"])
		put("isReasoner", root["isReasoner"])
		put("isFunction", root["isFunction"])
		put("maxInputTokens", root["maxInputTokens"])
		put("maxOutputTokens", root["maxOutputTokens"])
		put("timeout", root["timeout"])
	case "mcp-servers":
		put("baseUrl", contracts.FirstNonEmptyString(root["baseUrl"], root["base-url"], root["url"]))
		put("endpointPath", contracts.FirstNonEmptyString(root["endpointPath"], root["endpoint-path"], root["path"]))
		put("enabled", adminRegistryBool(root["enabled"], true))
		put("toolPrefix", contracts.FirstNonEmptyString(root["toolPrefix"], root["tool-prefix"]))
		if tools, ok := root["tools"].([]any); ok {
			out["toolCount"] = len(tools)
		}
	case "viewport-servers":
		put("baseUrl", contracts.FirstNonEmptyString(root["baseUrl"], root["base-url"], root["url"]))
		put("endpointPath", contracts.FirstNonEmptyString(root["endpointPath"], root["endpoint-path"], root["path"]))
		put("timeout", root["timeout"])
	}
	return out
}

func adminRegistryBool(raw any, fallback bool) bool {
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "yes", "1", "on", "enabled":
			return true
		case "false", "no", "0", "off", "disabled":
			return false
		}
	}
	return fallback
}

func adminRegistryDiagnostic(severity string, code string, message string, sourcePath string) api.AdminAgentDiagnostic {
	return api.AdminAgentDiagnostic{
		Severity:   severity,
		Code:       code,
		Message:    message,
		SourcePath: sourcePath,
	}
}

func adminRegistryHasError(diagnostics []api.AdminAgentDiagnostic) bool {
	for _, item := range diagnostics {
		if strings.EqualFold(item.Severity, "error") {
			return true
		}
	}
	return false
}

func firstDiagnosticMessage(diagnostics []api.AdminAgentDiagnostic, fallback string) string {
	for _, item := range diagnostics {
		if strings.TrimSpace(item.Message) != "" {
			return item.Message
		}
	}
	return fallback
}

func atomicWriteAdminRegistryFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
