package p_dynamic_custom

import "context"

type Storer interface {
	SaveSale(ctx context.Context, saleID string) (string, error)
}

func loopSaveSale(ctx context.Context, s Storer, items []string) {
	for _, item := range items {
		_, _ = s.SaveSale(ctx, item) // want `query-in-loop \[definite\]`
	}
}
