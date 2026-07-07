package cli

import (
	"path/filepath"

	"sermo/internal/config"
)

func testLoadConfigWithCatalog(catalogDir string) func(string, ...config.Option) (*config.Config, error) {
	return func(globalPath string, opts ...config.Option) (*config.Config, error) {
		opts = append([]config.Option{config.WithCatalogDirs(filepath.Clean(catalogDir))}, opts...)
		return config.Load(globalPath, opts...)
	}
}
