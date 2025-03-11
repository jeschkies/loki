package log

type LabelCategory int

const (
	StreamLabel LabelCategory = iota
	StructuredMetadataLabel
	ParsedLabel
	InvalidCategory

	numValidCategories = 3
)
