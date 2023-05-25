package logical

import (
	"fmt"
	"strconv"
	"strings"
)

type Graphviz struct{}

func NewGraphviz() *Graphviz {
	return &Graphviz{}
}

var _ Visitor[string] = &Graphviz{}

func (g *Graphviz) VisitAggregation(a *Aggregation) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf(`"%s" [label="Aggregation:%s\n%s"];`, a.GetID(), a.Details.Name(), a.Details.Parameters()))
	sb.WriteString("\n")
	if a.Child() != nil {
		sb.WriteString(fmt.Sprintf(`"%s" -> "%s";`, a.Child().GetID(), a.GetID()))
		sb.WriteString("\n")

		sb.WriteString(Dispatch[string](a.Child(), g))
	}
	return sb.String()
}

func (g *Graphviz) VisitCoalescence(c *Coalescence) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf(`"%s" [label="Coalescence"];`, c.ID))
	// TODO: show only defaultMaxDepth = 4 nodes
	for _, s := range c.Shards {
		sb.WriteString(fmt.Sprintf(`"%s" -> "%s";`, s.GetID(), c.GetID()))
		sb.WriteString("\n")

		sb.WriteString(Dispatch[string](s, g))
	}
	return sb.String()
}

func (g *Graphviz) VisitBinary(b *Binary) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf(`"%s" [label="Binary:%s"];`, b.GetID(), b.Kind))
	sb.WriteString("\n")
	if b.lhs != nil {
		sb.WriteString(fmt.Sprintf(`"%s" -> "%s";`, b.lhs.GetID(), b.GetID()))
		sb.WriteString("\n")

		sb.WriteString(Dispatch[string](b.lhs, g))
	}
	if b.rhs != nil {
		sb.WriteString(fmt.Sprintf(`"%s" -> "%s";`, b.rhs.GetID(), b.GetID()))
		sb.WriteString("\n")

		sb.WriteString(Dispatch[string](b.rhs, g))
	}

	return sb.String()
}

func (g *Graphviz) VisitFilter(f *Filter) string {
	var sb strings.Builder

	details := ""
	if f.Kind == "line" {
		details = fmt.Sprintf(`%s\"%s\"`, f.ty, f.match)
	}
	sb.WriteString(fmt.Sprintf(`"%s" [label="Filter:%s\n%s"];`, f.GetID(), f.Kind, details))
	sb.WriteString("\n")
	if f.Child() != nil {
		sb.WriteString(fmt.Sprintf(`"%s" -> "%s";`, f.Child().GetID(), f.GetID()))
		sb.WriteString("\n")

		sb.WriteString(Dispatch[string](f.Child(), g))
	}

	return sb.String()
}

func (g *Graphviz) VisitMap(m *Map) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf(`"%s" [label="Map:%s"];`, m.GetID(), m.Kind))
	sb.WriteString("\n")
	if m.Child() != nil {
		sb.WriteString(fmt.Sprintf(`"%s" -> "%s";`, m.Child().GetID(), m.GetID()))
		sb.WriteString("\n")

		sb.WriteString(Dispatch[string](m.Child(), g))
	}

	return sb.String()
}

func (g *Graphviz) VisitScan(s *Scan) string {
	var sb strings.Builder

	labels := strconv.Quote(s.Labels())
	sb.WriteString(fmt.Sprintf(`"%s" [label="Scan\nLabels: %s`, s.GetID(), labels[1:len(labels)-1]))
	if s.shard != nil {
		sb.WriteString(fmt.Sprintf("\nShard %d of %d", s.shard.Shard, s.shard.Of))
	}
	sb.WriteString(`"];`)
	sb.WriteString("\n")
	if s.Child() != nil {
		sb.WriteString(fmt.Sprintf(`"%s" -> "%s";`, s.Child().GetID(), s.GetID()))
		sb.WriteString("\n")

		sb.WriteString(Dispatch[string](s.Child(), g))
	}

	return sb.String()
}
