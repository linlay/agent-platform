package catalog

import "os"

func visitRuntimeEntries(root string, onMissing func(string), include func(name string, entry os.DirEntry) bool, visit func(name string, entry os.DirEntry)) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		if onMissing != nil {
			onMissing(root)
		}
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if include != nil && !include(name, entry) {
			continue
		}
		visit(name, entry)
	}
	return nil
}
