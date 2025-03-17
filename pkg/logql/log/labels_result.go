package log

import (
	"github.com/prometheus/prometheus/model/labels"
)

// LabelsResult is a computed labels result that contains the labels set with associated string and hash.
// The is mainly used for caching and returning labels computations out of pipelines and stages.
type LabelsResult interface {
	String() string
	Labels() labels.Labels
	Stream() labels.Labels
	StructuredMetadata() labels.Labels
	Parsed() labels.Labels
	Hash() uint64
}
