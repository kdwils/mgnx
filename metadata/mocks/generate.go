package mocks

//go:generate go run go.uber.org/mock/mockgen -destination=../mocks/mock_fetcher.go -package=mocks github.com/kdwils/mgnx/metadata Fetcher
