package apperrors

import "net/http"

type Definition struct {
	Code               Code
	Category           Category
	Scope              Scope
	HTTPStatus         int
	Retryable          bool
	UserSafeMessageKey string
}

var definitions = []Definition{
	def(CodeInvalidRequest, CategoryRequest, ScopeRequest, http.StatusBadRequest, false),
	def(CodeInvalidPayload, CategoryRequest, ScopeRequest, http.StatusBadRequest, false),
	def(CodeMissingRequiredField, CategoryRequest, ScopeRequest, http.StatusBadRequest, false),
	def(CodeInvalidField, CategoryRequest, ScopeRequest, http.StatusBadRequest, false),
	def(CodeMethodNotAllowed, CategoryRequest, ScopeRequest, http.StatusMethodNotAllowed, false),
	def(CodeInvalidLocale, CategoryRequest, ScopeRequest, http.StatusBadRequest, false),
	def(CodeUnsupportedOperation, CategoryRequest, ScopeRequest, http.StatusBadRequest, false),
	def(CodeTimeContractViolation, CategoryRequest, ScopeRequest, http.StatusUnprocessableEntity, false),
	def(CodeChatStorageSchemaViolation, CategoryChatRun, ScopeChat, http.StatusUnprocessableEntity, false),

	def(CodeUnauthorized, CategoryAuth, ScopeRequest, http.StatusUnauthorized, false),
	def(CodeForbidden, CategoryAuth, ScopeRequest, http.StatusForbidden, false),
	def(CodeAgentForbidden, CategoryAuth, ScopeRequest, http.StatusForbidden, false),
	def(CodeChannelForbidden, CategoryAuth, ScopeRequest, http.StatusForbidden, false),

	def(CodeDuplicateID, CategoryProtocol, ScopeConnection, http.StatusConflict, false),
	def(CodeDuplicateStream, CategoryProtocol, ScopeStream, http.StatusConflict, false),
	def(CodeDuplicateObserve, CategoryProtocol, ScopeStream, http.StatusConflict, false),
	def(CodeTooManyStreams, CategoryProtocol, ScopeStream, http.StatusTooManyRequests, true),
	def(CodeTooManyObservers, CategoryProtocol, ScopeStream, http.StatusTooManyRequests, true),
	def(CodeSeqExpired, CategoryProtocol, ScopeStream, http.StatusConflict, false),
	def(CodeLegacySEQExpired, CategoryProtocol, ScopeStream, http.StatusConflict, false),

	def(CodeAgentNotFound, CategoryCatalog, ScopeRequest, http.StatusNotFound, false),
	def(CodeModelNotFound, CategoryCatalog, ScopeModel, http.StatusNotFound, false),
	def(CodeProviderNotConfigured, CategoryCatalog, ScopeModel, http.StatusBadGateway, false),
	def(CodeModelRegistryUnavailable, CategoryCatalog, ScopeModel, http.StatusServiceUnavailable, true),
	def(CodeAgentRegistryUnavailable, CategoryCatalog, ScopeRequest, http.StatusServiceUnavailable, true),
	def(CodeConfigurationError, CategoryCatalog, ScopeSystem, http.StatusInternalServerError, false),
	def(CodePlanningModeUnsupported, CategoryCatalog, ScopeRequest, http.StatusBadRequest, false),
	def(CodeACPBridgeNotConfigured, CategoryCatalog, ScopeProxy, http.StatusBadGateway, false),
	def(CodeProxyConfigMissing, CategoryCatalog, ScopeProxy, http.StatusBadGateway, false),

	def(CodeChatNotFound, CategoryChatRun, ScopeChat, http.StatusNotFound, false),
	def(CodeRunNotFound, CategoryChatRun, ScopeRun, http.StatusNotFound, false),
	def(CodeActiveRunConflict, CategoryChatRun, ScopeRun, http.StatusConflict, false),
	def(CodeRunAlreadyFinished, CategoryChatRun, ScopeRun, http.StatusConflict, false),
	def(CodeRunCancelled, CategoryChatRun, ScopeRun, http.StatusConflict, false),
	def(CodeRunTimeout, CategoryChatRun, ScopeRun, http.StatusGatewayTimeout, true),
	def(CodeEventBusUnavailable, CategoryChatRun, ScopeRun, http.StatusInternalServerError, true),
	def(CodeObserverAttachFailed, CategoryChatRun, ScopeStream, http.StatusInternalServerError, true),
	def(CodeStreamFailed, CategoryChatRun, ScopeRun, http.StatusInternalServerError, false),
	def(CodeRunError, CategoryChatRun, ScopeRun, http.StatusInternalServerError, false),
	def(CodeRunInterrupted, CategoryChatRun, ScopeRun, http.StatusConflict, false),

	def(CodeProviderRequestFailed, CategoryModel, ScopeModel, http.StatusBadGateway, false),
	def(CodeProviderNetworkError, CategoryModel, ScopeModel, http.StatusBadGateway, true),
	def(CodeProviderTimeout, CategoryModel, ScopeModel, http.StatusGatewayTimeout, true),
	def(CodeProviderAuthFailed, CategoryModel, ScopeModel, http.StatusUnauthorized, false),
	def(CodeProviderPermissionDenied, CategoryModel, ScopeModel, http.StatusForbidden, false),
	def(CodeProviderQuotaExhausted, CategoryModel, ScopeModel, http.StatusTooManyRequests, false),
	def(CodeProviderRateLimited, CategoryModel, ScopeModel, http.StatusTooManyRequests, true),
	def(CodeProviderModelNotFound, CategoryModel, ScopeModel, http.StatusNotFound, false),
	def(CodeProviderContextLengthExceeded, CategoryModel, ScopeModel, http.StatusBadRequest, false),
	def(CodeProviderContentFilter, CategoryModel, ScopeModel, http.StatusBadRequest, false),
	def(CodeProviderBadRequest, CategoryModel, ScopeModel, http.StatusBadRequest, false),
	def(CodeProviderBadResponse, CategoryModel, ScopeModel, http.StatusBadGateway, true),
	def(CodeProviderUnavailable, CategoryModel, ScopeModel, http.StatusServiceUnavailable, true),
	def(CodeProviderStreamFailed, CategoryModel, ScopeModel, http.StatusBadGateway, true),
	def(CodeProviderStreamInvalid, CategoryModel, ScopeModel, http.StatusBadGateway, true),
	def(CodeMissingToolCallID, CategoryModel, ScopeModel, http.StatusInternalServerError, false),

	def(CodeProxyRequestFailed, CategoryProxy, ScopeProxy, http.StatusBadGateway, true),
	def(CodeProxyUpstreamError, CategoryProxy, ScopeProxy, http.StatusBadGateway, true),
	def(CodeProxyTimeout, CategoryProxy, ScopeProxy, http.StatusGatewayTimeout, true),
	def(CodeProxyBadResponse, CategoryProxy, ScopeProxy, http.StatusBadGateway, true),
	def(CodeProxyStreamingUnsupported, CategoryProxy, ScopeProxy, http.StatusInternalServerError, false),

	def(CodeToolNotFound, CategoryTool, ScopeTool, http.StatusNotFound, false),
	def(CodeToolFailed, CategoryTool, ScopeTool, http.StatusInternalServerError, false),
	def(CodeToolTimeout, CategoryTool, ScopeTool, http.StatusGatewayTimeout, true),
	def(CodeToolArgsInvalid, CategoryTool, ScopeTool, http.StatusBadRequest, false),
	def(CodeToolCallsExceeded, CategoryTool, ScopeTool, http.StatusBadRequest, false),
	def(CodeExternalToolCallFailed, CategoryTool, ScopeTool, http.StatusBadGateway, true),
	def(CodeMCPCallFailed, CategoryTool, ScopeTool, http.StatusBadGateway, true),
	def(CodeSubAgentFailed, CategoryTool, ScopeTask, http.StatusInternalServerError, false),
	def(CodeTaskFailed, CategoryTool, ScopeTask, http.StatusInternalServerError, false),
	def(CodeTaskExecutionError, CategoryTool, ScopeTask, http.StatusInternalServerError, false),
	def(CodePlanContextUnavailable, CategoryTool, ScopeRun, http.StatusServiceUnavailable, false),
	def(CodePlanningNotCreated, CategoryModel, ScopeRun, http.StatusInternalServerError, false),
	def(CodePlanNotCreated, CategorySystem, ScopeRun, http.StatusInternalServerError, false),
	def(CodeToolCallsNotAllowed, CategorySystem, ScopeRun, http.StatusInternalServerError, false),
	def(CodeBTWToolLimitReached, CategoryTool, ScopeTool, http.StatusInternalServerError, false),
	def(CodeTeamMemberFailed, CategorySystem, ScopeTask, http.StatusInternalServerError, false),

	def(CodeBudgetExceeded, CategoryPolicy, ScopeRun, http.StatusBadRequest, false),
	def(CodeModelCallsExceeded, CategoryPolicy, ScopeModel, http.StatusBadRequest, false),
	def(CodePolicyDenied, CategoryPolicy, ScopeTool, http.StatusForbidden, false),
	def(CodeHitlRejected, CategoryPolicy, ScopeTool, http.StatusForbidden, false),
	def(CodeHitlRejectedWithFeedback, CategoryPolicy, ScopeTool, http.StatusForbidden, false),
	def(CodeHitlTimeout, CategoryPolicy, ScopeTool, http.StatusGatewayTimeout, true),
	def(CodeApprovalTimeout, CategoryPolicy, ScopeTool, http.StatusGatewayTimeout, true),
	def(CodeUserDismissed, CategoryPolicy, ScopeFrontendSubmit, http.StatusConflict, false),
	def(CodeFrontendSubmitTimeout, CategoryPolicy, ScopeFrontendSubmit, http.StatusGatewayTimeout, true),
	def(CodeFrontendSubmitInvalidPayload, CategoryPolicy, ScopeFrontendSubmit, http.StatusBadRequest, false),
	def(CodeFrontendToolHandlerNotRegistered, CategoryPolicy, ScopeFrontendSubmit, http.StatusInternalServerError, false),

	def(CodeResourceNotFound, CategoryResource, ScopeResource, http.StatusNotFound, false),
	def(CodeResourceForbidden, CategoryResource, ScopeResource, http.StatusForbidden, false),
	def(CodeResourceTicketRequired, CategoryResource, ScopeResource, http.StatusUnauthorized, false),
	def(CodeResourceTicketChatMismatch, CategoryResource, ScopeResource, http.StatusForbidden, false),
	def(CodeResourceReadFailed, CategoryResource, ScopeResource, http.StatusInternalServerError, false),
	def(CodeResourcePushFailed, CategoryResource, ScopeResource, http.StatusBadGateway, true),
	def(CodeUploadFailed, CategoryResource, ScopeResource, http.StatusBadGateway, true),
	def(CodeDownloadFailed, CategoryResource, ScopeResource, http.StatusBadGateway, true),
	def(CodeInvalidUploadMetadata, CategoryResource, ScopeResource, http.StatusBadRequest, false),
	def(CodeToolResultNotFound, CategoryResource, ScopeResource, http.StatusNotFound, false),
	def(CodeToolResultForbidden, CategoryResource, ScopeResource, http.StatusForbidden, false),
	def(CodeToolResultAccessDenied, CategoryResource, ScopeResource, http.StatusForbidden, false),
	def(CodeFileHistoryUnavailable, CategoryResource, ScopeResource, http.StatusServiceUnavailable, true),
	def(CodeFileHistoryNotFound, CategoryResource, ScopeResource, http.StatusNotFound, false),
	def(CodeInvalidFileHistoryRequest, CategoryResource, ScopeResource, http.StatusBadRequest, false),

	def(CodeTerminalUnavailable, CategoryTerminal, ScopeTerminal, http.StatusServiceUnavailable, true),
	def(CodeTerminalNotFound, CategoryTerminal, ScopeTerminal, http.StatusNotFound, false),
	def(CodeTerminalUnsupported, CategoryTerminal, ScopeTerminal, http.StatusNotImplemented, false),
	def(CodeUnsupported, CategoryTerminal, ScopeTerminal, http.StatusNotImplemented, false),

	def(CodeMemoryDisabled, CategoryMemory, ScopeMemory, http.StatusServiceUnavailable, false),
	def(CodeMemoryStoreUnavailable, CategoryMemory, ScopeMemory, http.StatusServiceUnavailable, true),
	def(CodeMemoryHistoryUnavailable, CategoryMemory, ScopeMemory, http.StatusServiceUnavailable, true),
	def(CodeMemoryNotFound, CategoryMemory, ScopeMemory, http.StatusNotFound, false),
	def(CodeEmbeddingProviderNotConfigured, CategoryMemory, ScopeMemory, http.StatusServiceUnavailable, false),
	def(CodeMemoryOperationFailed, CategoryMemory, ScopeMemory, http.StatusInternalServerError, false),

	def(CodeArchiveUnavailable, CategoryArchive, ScopeArchive, http.StatusServiceUnavailable, true),
	def(CodeArchiveNotFound, CategoryArchive, ScopeArchive, http.StatusNotFound, false),
	def(CodeArchiveOperationFailed, CategoryArchive, ScopeArchive, http.StatusInternalServerError, false),

	def(CodeAutomationUnavailable, CategoryAutomation, ScopeAutomation, http.StatusServiceUnavailable, true),
	def(CodeAutomationNotFound, CategoryAutomation, ScopeAutomation, http.StatusNotFound, false),
	def(CodeAutomationInvalid, CategoryAutomation, ScopeAutomation, http.StatusBadRequest, false),
	def(CodeAutomationExecutionStoreUnavailable, CategoryAutomation, ScopeAutomation, http.StatusServiceUnavailable, true),
	def(CodeAutomationIDAllocateFailed, CategoryAutomation, ScopeAutomation, http.StatusInternalServerError, false),

	def(CodeInternalError, CategorySystem, ScopeSystem, http.StatusInternalServerError, false),
	def(CodeServiceUnavailable, CategorySystem, ScopeSystem, http.StatusServiceUnavailable, true),
	def(CodeUnavailable, CategorySystem, ScopeSystem, http.StatusServiceUnavailable, true),
	def(CodeStorageFailed, CategorySystem, ScopeSystem, http.StatusInternalServerError, false),
	def(CodeChatStoreUnavailable, CategorySystem, ScopeChat, http.StatusServiceUnavailable, true),
	def(CodeSkillCandidateStoreUnavailable, CategorySystem, ScopeSystem, http.StatusServiceUnavailable, true),
	def(CodeNotImplemented, CategorySystem, ScopeSystem, http.StatusNotImplemented, false),
	def(CodeNotFound, CategorySystem, ScopeRequest, http.StatusNotFound, false),
	def(CodeTimeout, CategoryTimeout, ScopeRun, http.StatusGatewayTimeout, true),
}

var definitionsByCode = buildDefinitions(definitions)

func def(code Code, category Category, scope Scope, status int, retryable bool) Definition {
	return Definition{
		Code:               code,
		Category:           category,
		Scope:              scope,
		HTTPStatus:         status,
		Retryable:          retryable,
		UserSafeMessageKey: string(code),
	}
}

func buildDefinitions(items []Definition) map[Code]Definition {
	out := make(map[Code]Definition, len(items))
	for _, item := range items {
		if item.Code == "" {
			panic("apperrors: empty error code")
		}
		if _, exists := out[item.Code]; exists {
			panic("apperrors: duplicate error code " + string(item.Code))
		}
		out[item.Code] = item
	}
	return out
}

func Lookup(code Code) (Definition, bool) {
	definition, ok := definitionsByCode[code]
	return definition, ok
}

func Definitions() []Definition {
	return append([]Definition(nil), definitions...)
}
