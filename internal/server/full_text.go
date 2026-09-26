package server

import "agent-platform/internal/runtime/runexec"

type queryFullTextBuilder = runexec.FullTextBuilder

var newQueryFullTextBuilder = runexec.NewFullTextBuilder
var isDiscardIncompleteModelTurnRecovery = runexec.IsDiscardIncompleteModelTurnRecovery
var stringSetFromAny = runexec.StringSetFromAny
var firstNonBlankString = runexec.FirstNonBlankString
var formatFullTextValue = runexec.FormatFullTextValue
