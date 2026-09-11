package server

import (
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
)

func (s *Server) recordCatalogMetrics(c *catalog.Catalog) {
	upstream, aliases := 0, 0
	for _, entry := range c.Entries() {
		if entry.Alias {
			aliases++
		} else {
			upstream++
		}
	}
	s.metrics.CatalogSuccess(time.Now(), upstream, aliases)
}
