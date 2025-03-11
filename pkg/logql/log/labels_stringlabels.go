//go:build stringlabels

package log

import (
	"github.com/prometheus/prometheus/model/labels"
)


type LabelsBuilder struct {
	*labels.ScratchBuilder

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
