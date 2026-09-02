package chat

// AtomicReplaceFile exposes the cross-platform durable replacement primitive
// to other trusted Platform storage domains.
func AtomicReplaceFile(source string, target string) error {
	return atomicReplaceFile(source, target)
}
