package app

import (
	"context"
	"github.com/Syt100/mailpulse/internal/domain"
)

type Processor struct{ store domain.BatchStore }

func NewProcessor(store domain.BatchStore) *Processor { return &Processor{store} }
func (p *Processor) Process(ctx context.Context, b domain.Batch, rules []domain.Rule) error {
	ds := make([]domain.Decision, 0, len(b.Events))
	for _, e := range b.Events {
		ds = append(ds, domain.Decide(e, func() string {
			if b.Alias != "" {
				return b.Alias
			}
			return e.AccountID
		}(), rules))
	}
	return p.store.CommitBatch(ctx, b, ds)
}
