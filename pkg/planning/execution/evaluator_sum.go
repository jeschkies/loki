package execution

import (
	"sort"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"

	"github.com/grafana/loki/pkg/logql/syntax" // TODO: avoid syntax package
)

type SumAggregationEvaluator struct {
	upstream StepEvaluator
	grouping syntax.Grouping

	lb  *labels.Builder
	buf []byte
}

func NewSumAggregationEvaluator(grouping syntax.Grouping, upstream StepEvaluator) StepEvaluator {
	sort.Strings(grouping.Groups)

	return &SumAggregationEvaluator{
		upstream: upstream,
		grouping: grouping,
		lb:       labels.NewBuilder(nil),
		buf:      make([]byte, 0, 1024),
	}
}

var _ StepEvaluator = &SumAggregationEvaluator{}

type groupedAggregation struct {
	labels     labels.Labels
	value      float64
	mean       float64
	groupCount int
}

func (e *SumAggregationEvaluator) Next() (bool, Value) {
	next, value := e.upstream.Next()
	vec := value.(Vector).vec
	ts := value.(Vector).ts

	if !next {
		return false, Vector{}
	}
	result := map[uint64]*groupedAggregation{}
	for _, s := range vec {
		metric := s.Metric

		var groupingKey uint64
		if e.grouping.Without {
			groupingKey, e.buf = metric.HashWithoutLabels(e.buf, e.grouping.Groups...)
		} else {
			groupingKey, e.buf = metric.HashForLabels(e.buf, e.grouping.Groups...)
		}
		group, ok := result[groupingKey]
		// Add a new group if it doesn't exist.
		if !ok {
			var m labels.Labels

			if e.grouping.Without {
				e.lb.Reset(metric)
				e.lb.Del(e.grouping.Groups...)
				e.lb.Del(labels.MetricName)
				m = e.lb.Labels()
			} else {
				m = make(labels.Labels, 0, len(e.grouping.Groups))
				for _, l := range metric {
					for _, n := range e.grouping.Groups {
						if l.Name == n {
							m = append(m, l)
							break
						}
					}
				}
				sort.Sort(m)
			}
			result[groupingKey] = &groupedAggregation{
				labels:     m,
				value:      s.F,
				mean:       s.F,
				groupCount: 1,
			}

			continue
		}
		group.value += s.F
	}
	vec = vec[:0]
	for _, aggr := range result {
		vec = append(vec, promql.Sample{
			Metric: aggr.labels,
			T:      ts,
			F:      aggr.value,
		})
	}
	return next, Vector{ts: ts, vec: vec} 
}

func (e *SumAggregationEvaluator) Close() error {
	return e.upstream.Close()
}

func (e *SumAggregationEvaluator) Error() error {
	return e.upstream.Error()
}
