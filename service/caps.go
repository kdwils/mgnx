package service

func (s *Service) Caps() XMLCaps {
	return newXMLCaps(s.caps())
}

func (s *Service) caps() CapsResponse {
	return CapsResponse{
		MaxLimit:     100,
		DefaultLimit: 50,
		SearchModes: map[string]SearchMode{
			"search": {
				Available:       true,
				SupportedParams: []string{"q"},
			},
			"movie-search": {
				Available:       true,
				SupportedParams: []string{"q", "imdbid"},
			},
			"tv-search": {
				Available:       true,
				SupportedParams: []string{"q", "season", "ep", "imdbid"},
			},
		},
		Categories: []Category{
			{
				ID:   CatMovies,
				Name: "Movies",
				Subcategories: []Subcategory{
					{ID: CatMoviesSD, Name: "SD"},
					{ID: CatMoviesHD, Name: "HD"},
					{ID: CatMoviesUHD, Name: "UHD"},
					{ID: CatMoviesBluRay, Name: "BluRay"},
					{ID: CatMoviesForeign, Name: "Foreign"},
					{ID: CatMoviesOther, Name: "Other"},
					{ID: CatMovies3D, Name: "3D"},
				},
			},
			{
				ID:   CatTV,
				Name: "TV",
				Subcategories: []Subcategory{
					{ID: CatTVSD, Name: "SD"},
					{ID: CatTVHD, Name: "HD"},
					{ID: CatTVUHD, Name: "UHD"},
					{ID: CatTVAnime, Name: "Anime"},
					{ID: CatTVDocumentary, Name: "Documentary"},
				},
			},
		},
	}
}
