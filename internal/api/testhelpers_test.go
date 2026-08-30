package api

import "github.com/andrii/ephyra/internal/aggregate"

func sampleAgg() aggregate.LibraryAggregates {
	return aggregate.LibraryAggregates{
		Totals: map[string]float64{
			"items.total": 6, "items.Series": 3,
			"runtime_sec.total": 100, "bytes.total": 200,
		},
	}
}
