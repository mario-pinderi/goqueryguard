package p_dynamic_custom_possible

import "context"

type Updater interface {
	UpdateThing(ctx context.Context, id string) error
}

func loopUpdateThing(ctx context.Context, u Updater, ids []string) {
	for _, id := range ids {
		_ = u.UpdateThing(ctx, id) // want `query-in-loop \[possible\]`
	}
}
