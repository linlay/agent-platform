package query

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/runtime/runexec"
)

func timeContractStatusError(err error) *statusError {
	if !isTimeContractViolation(err) {
		return nil
	}
	code := "time_contract_violation"
	message := timeContractViolationMessage
	if chat.IsJSONLSchemaViolation(err) {
		code = chat.ChatStorageSchemaViolationCode
		message = chatStorageSchemaViolationMessage
	}
	return &statusError{
		Status:  422,
		Code:    code,
		Message: message,
		Data:    timeContractErrorData(err),
	}
}

const timeContractViolationMessage = "time contract violation"

const chatStorageSchemaViolationMessage = "chat storage schema violation"

var timeContractErrorData = runexec.TimeContractErrorData
var isTimeContractViolation = runexec.IsTimeContractViolation
