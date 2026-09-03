package api

import "github.com/andriykohut/ephyra/internal/aggregate"

func sampleAgg() aggregate.LibraryAggregates {
	return aggregate.LibraryAggregates{
		Totals: map[string]float64{
			"items.total": 6, "items.Series": 3,
			"runtime_sec.total": 100, "bytes.total": 200,
			"tags.tagged_items": 4, "tags.total_items": 6,
		},
		TagsTop:     []aggregate.LabeledCount{{Label: "heist", Count: 4}},
		TagCoverage: aggregate.TagCoverage{ItemsTagged: 4, ItemsTotal: 6},
		TagPairs:    []aggregate.TagPair{{A: "heist", B: "vault", Items: 3}},
	}
}
