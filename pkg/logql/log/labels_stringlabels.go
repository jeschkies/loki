//go:build stringlabels

package log

import (
	"github.com/prometheus/prometheus/model/labels"
)

// BaseLabelsBuilder is a label builder used by pipeline and stages.
// Only one base builder is used and it contains cache for each LabelsBuilders.
type BaseLabelsBuilder struct {
}

// NewBaseLabelsBuilder creates a new base labels builder.
func NewBaseLabelsBuilder() *BaseLabelsBuilder {
	return &BaseLabelsBuilder{}
}

// ForLabels creates a labels builder for a given labels set as base.
// The labels cache is shared across all created LabelsBuilders.
func (b *BaseLabelsBuilder) ForLabels(lbs labels.Labels, hash uint64) *LabelsBuilder {
	return &LabelsBuilder{
		ScratchBuilder: labels.NewScratchBuilder(lbs.Len()),
	}
}
type LabelsBuilder struct {
	labels.ScratchBuilder

	// nolint:structcheck
	// https://github.com/golangci/golangci-lint/issues/826
	err string
	// nolint:structcheck
	errDetails string
}

func (b *LabelsBuilder) SetBytes(k, v []byte) {
	b.ScratchBuilder.UnsafeAddBytes(k, v)
}

// Get, Map and Put might not be required.
func (b *LabelsBuilder) Get(key string) (string, bool) {
	panic("not implemented")
}

func (b *LabelsBuilder) Map() (map[string]string, bool) {
	panic("not implemented")
}

func (b *LabelsBuilder) Del(key string) *LabelsBuilder {
	panic("not implemented")
}

func (b *LabelsBuilder) Set(category LabelCategory, n, v string) *LabelsBuilder {
	b.ScratchBuilder.Add(n, v)
	return b
}

func (b *LabelsBuilder) Add(category LabelCategory, lbls labels.Labels) *LabelsBuilder {
	lbls.Range(func(l labels.Label) {
		b.ScratchBuilder.Add(l.Name, l.Value)
	})
	return b
}

func (b *LabelsBuilder) IntoMap(m map[string]string) {
	panic("not implemented")
}

func (b *LabelsBuilder) GetWithCategory(key string) (string, LabelCategory, bool) {
	panic("not implemented")
}

// SetErr sets the error label.
func (b *LabelsBuilder) SetErr(err string) *LabelsBuilder {
	b.err = err
	return b
}

// GetErr return the current error label value.
func (b *LabelsBuilder) GetErr() string {
	return b.err
}

// HasErr tells if the error label has been set.
func (b *LabelsBuilder) HasErr() bool {
	return b.err != ""
}

func (b *LabelsBuilder) SetErrorDetails(desc string) *LabelsBuilder {
	b.errDetails = desc
	return b
}

func (b *LabelsBuilder) ResetError() *LabelsBuilder {
	b.err = ""
	return b
}

func (b *LabelsBuilder) ResetErrorDetails() *LabelsBuilder {
	b.errDetails = ""
	return b
}

func (b *LabelsBuilder) GetErrorDetails() string {
	return b.errDetails
}

func (b *LabelsBuilder) HasErrorDetails() bool {
	return b.errDetails != ""
}

func (b *LabelsBuilder) UnsortedLabels(buf labels.Labels, categories ...LabelCategory) labels.Labels {
	// TODO: this is not performant. Use columnar builder instead
	return b.ScratchBuilder.Labels()
}

func (b *LabelsBuilder) LabelsResult() LabelsResult {
	return &labelsResult{
		labels: b.ScratchBuilder.Labels(),
	}
}

type labelsResult struct {
	labels labels.Labels
}

func (l *labelsResult) String() string {
	return l.labels.String()
}

func (l *labelsResult) Labels() labels.Labels {
	return l.labels
}

func (l *labelsResult) Hash() uint64 {
	return l.labels.Hash()
}


func (l *labelsResult) Stream() labels.Labels {
	return l.labels
}

func (l *labelsResult) StructuredMetadata() labels.Labels {
	// TODO: distinguis
	return l.labels
}

func (l *labelsResult) Parsed() labels.Labels {
	// TODO: distinguish between parsed and structured metadata
	return l.labels
}
