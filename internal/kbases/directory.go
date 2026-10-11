package kbases

import "agent-platform/internal/knowledge/libraryinfo"

func (s *Service) LibraryOption(id string) (libraryinfo.LibraryOption, error) {
	d, err := s.Get(id)
	if err != nil {
		return libraryinfo.LibraryOption{}, err
	}
	return s.libraryOption(d), nil
}

func (s *Service) libraryOption(d Definition) libraryinfo.LibraryOption {
	option := libraryinfo.LibraryOption{ID: d.ID, Name: d.Name, Description: d.Description,
		Collections: make([]libraryinfo.CollectionSummary, 0, len(d.Collections)), Indexing: d.Indexing, Degraded: d.Degraded}
	for _, c := range d.Collections {
		option.Collections = append(option.Collections, libraryinfo.CollectionSummary{Name: c.Name, Description: c.Description})
	}
	_, _, release, err := s.Acquire(d.ID)
	if err != nil {
		option.UnavailableReason = err.Error()
	} else {
		option.Readable = true
		release()
	}
	return option
}

func (s *Service) LibraryOptions() ([]libraryinfo.LibraryOption, error) {
	definitions, err := s.List()
	if err != nil {
		return nil, err
	}
	options := make([]libraryinfo.LibraryOption, 0, len(definitions))
	for _, d := range definitions {
		options = append(options, s.libraryOption(d))
	}
	return options, nil
}
