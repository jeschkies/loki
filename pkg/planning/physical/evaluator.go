package physical

import (
	"github.com/prometheus/prometheus/promql"

	"github.com/grafana/loki/pkg/planning/logical"
)

// StepEvaluator evaluate a single step of a query.
type StepEvaluator interface {
	// while Next returns a promql.Value, the only acceptable types are Scalar and Vector.
	Next() (ok bool, ts int64, vec promql.Vector)
	// Close all resources used.
	Close() error
	// Reports any error
	Error() error
}

func NewStepEvaluator(p *logical.Plan) StepEvaluator {
	return nil
}

type Builder struct{}

var _ logical.Visitor = &Builder{}

func (b *Builder) VisitAggregation(*logical.Aggregation) {

}

func (b *Builder) VisitCoalescence(*logical.Coalescence)
func (b *Builder) VisitBinary(*logical.Binary)
func (b *Builder) VisitFilter(*logical.Filter)
func (b *Builder) VisitMap(*logical.Map)
func (b *Builder) VisitScan(*logical.Scan) {
	// TODO: build querier
}
