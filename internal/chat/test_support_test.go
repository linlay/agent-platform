package chat

func NewArchiveStore(chatsRoot string) (*ArchiveStore, error) {
	return newArchiveStore(chatsRoot, false)
}

func NewFileStore(root string) (*FileStore, error) {
	return newFileStore(root, false)
}

func readJSONLines(path string) ([]map[string]any, error) {
	return readJSONLinesWithNumber(path, false)
}
