package memory

func NewSQLiteStore(root string, dbFileName string) (*SQLiteStore, error) {
	return newSQLiteStore(root, dbFileName, false)
}
