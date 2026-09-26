package server

import (
	"agent-platform/internal/runtime/proxy"
)

var materializeProxyFileReference = proxy.MaterializeProxyFileReference
var resolveProxyFileSource = proxy.ResolveProxyFileSource
var normalizeProxyReferencePath = proxy.NormalizeProxyReferencePath
var normalizeProxyReferenceURL = proxy.NormalizeProxyReferenceURL
var sameURLOrigin = proxy.SameURLOrigin
var resourceURLForFileParam = proxy.ResourceURLForFileParam
var safePathSegment = proxy.SafePathSegment
var deduplicateProxyInputPath = proxy.DeduplicateProxyInputPath
var sameFileContent = proxy.SameFileContent
var sameFilesystemPath = proxy.SameFilesystemPath
var copyProxyFile = proxy.CopyProxyFile
var guessProxyMimeType = proxy.GuessProxyMimeType
var isPathOutsideBase = proxy.IsPathOutsideBase
