package log

import (
	"math/bits"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/cespare/xxhash/v2"
	"github.com/prometheus/prometheus/model/labels"
)

// defaultMaxCachedLabelSets is how many label sets the cache of a batch remembers.
const defaultMaxCachedLabelSets = MaxInternedStrings

// LabelsResult returns the labels of row, the same LabelsResult that
// StreamPipeline.Process returns for the entry. It is meant to be called after
// the pipeline ran, for the rows that are left in Selection, and only then pays
// for building labels.
//
// A row whose structured metadata and parsed pairs were seen before gets the
// result that was built for them, like the cache of a LabelsBuilder does. Building
// is the expensive part: sorting, encoding, and later the string. The results are
// cached in LabelsCache, or in a cache created for the batch if there is none.
//
// The result does not refer to the batch, so it stays valid after the batch and
// its columns are released. That relies on ScratchBuilder copying the strings of
// the columns into the labels it builds, which the default stringlabels
// implementation does. With the slicelabels build tag it does not, and the result
// would point into released memory.
//
// Like LabelsBuilder.LabelsResult, a row that has neither structured metadata nor
// parsed labels gets BaseLabels itself, with its string and hash already computed.
//
// TODO: a parsed label that has the name of a stream label or of structured
// metadata should be renamed with the _extracted suffix, as Process does.
// TODO: a key that occurs more than once in a row of ParsedLabelsView should
// only count once, the first value wins as it does in Process.
func (b *ArrowBatch) LabelsResult(row int) LabelsResult {
	key, hasPairs := b.labelsKey(row)
	if !hasPairs {
		if b.BaseLabels == nil {
			return EmptyLabelsResult
		}
		return b.BaseLabels
	}

	if b.LabelsCache == nil {
		b.LabelsCache = NewLabelsResultCache(defaultMaxCachedLabelSets)
	}
	if res, ok := b.LabelsCache.get(key); ok {
		return res
	}
	res := b.buildLabelsResult(row)
	b.LabelsCache.put(key, res)
	return res
}

// LabelsResultCache remembers the results of ArrowBatch.LabelsResult by the content of
// the pairs of a row.
//
// It must only be used for rows of one stream, as the result includes the labels of
// the stream. It is not safe for concurrent use, like the cache of a LabelsBuilder.
type LabelsResultCache struct {
	results map[uint64]*columnarLabelsResult
	max     int
}

// NewLabelsResultCache returns a cache for at most max results. Once it is full it
// does not store new results, but it keeps returning the ones it has. With a max
// of zero it never stores one, which turns caching off.
func NewLabelsResultCache(max int) *LabelsResultCache {
	return &LabelsResultCache{max: max}
}

// Len returns the number of results in the cache.
func (c *LabelsResultCache) Len() int { return len(c.results) }

func (c *LabelsResultCache) get(key uint64) (*columnarLabelsResult, bool) {
	res, ok := c.results[key] // reading a nil map is fine
	return res, ok
}

func (c *LabelsResultCache) put(key uint64, res *columnarLabelsResult) {
	if len(c.results) >= c.max {
		return
	}
	if c.results == nil {
		c.results = make(map[uint64]*columnarLabelsResult)
	}
	c.results[key] = res
}

// labelsKey returns a hash of the structured metadata and parsed pairs of row, and
// whether the row has any.
//
// The pairs are hashed in the order they have in the row, not sorted: sorting is
// what the cache is meant to avoid. The same pairs in another order are another
// key, which only costs a second entry, and never gives a wrong result.
//
// As in the cache of a LabelsBuilder, a hit is not checked for equality. Two
// different sets with the same 64 bit key would share a result.
func (b *ArrowBatch) labelsKey(row int) (key uint64, hasPairs bool) {
	h := uint64(labelsKeySeed)
	pairs := 0

	switch {
	case b.StructuredMetadataView != nil:
		offs, keys, vals := b.StructuredMetadataColumns()
		var n int
		h, n = hashPairs(h, offs, keys, vals, row)
		pairs += n
	case row < len(b.StructuredMetadata):
		b.StructuredMetadata[row].Range(func(l labels.Label) {
			h = hashString(hashString(h, l.Name), l.Value)
			pairs++
		})
	}
	// The pairs of one category must not be mistaken for those of the other.
	h = mixHash(h, labelsKeySeparator)
	if b.ParsedLabelsView != nil {
		offs, keys, vals := b.ParsedLabelsColumns()
		var n int
		h, n = hashPairs(h, offs, keys, vals, row)
		pairs += n
	}
	return h, pairs > 0
}

const (
	labelsKeySeed      = 0x9E3779B97F4A7C15
	labelsKeySeparator = 0xC2B2AE3D27D4EB4F
)

// hashPairs mixes the pairs of row into h and returns the number of pairs.
func hashPairs(h uint64, offs []int32, keys, vals *array.StringView, row int) (uint64, int) {
	from, to := int(offs[row]), int(offs[row+1])
	for i := from; i < to; i++ {
		h = hashString(hashString(h, keys.Value(i)), vals.Value(i))
	}
	return h, to - from
}

func hashString(h uint64, s string) uint64 {
	return mixHash(h, xxhash.Sum64String(s))
}

func mixHash(h, x uint64) uint64 {
	return (bits.RotateLeft64(h, 31) ^ x) * 0x9E3779B97F4A7C15
}
