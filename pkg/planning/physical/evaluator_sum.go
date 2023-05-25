package physical

import (
	"github.com/prometheus/prometheus/promql"

	"github.com/grafana/loki/pkg/planning/logical"
)

type SumAggregationEvaluator struct {
	upstream logical.Operator
}

var _ StepEvaluator = &SumAggregationEvaluator{}

func (e *SumAggregationEvaluator) Next() (ok bool, ts int64, vec promql.Vector) {
	return
}

func (e *SumAggregationEvaluator) Close() error {
	return nil
}

func (e *SumAggregationEvaluator) Error() error {
	return nil
}
