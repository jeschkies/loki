package logical

import (
	"fmt"
	"io"
	"strconv"
)

type Graphviz struct {
	io.StringWriter
}

func NewGraphviz(w io.StringWriter) *Graphviz {
	return &Graphviz{StringWriter: w}
}

var _ Visitor = &Graphviz{}

func (g *Graphviz) visitAggregation(a *Aggregation) {
	g.WriteString(fmt.Sprintf(`"%s" [label="Aggregation:%s\n%s"];`, a.GetID(), a.Details.Name(), a.Details.Parameters()))
	g.WriteString("\n")
	if a.Child() != nil {
		g.WriteString(fmt.Sprintf(`"%s" -> "%s";`, a.Child().GetID(), a.GetID()))
		g.WriteString("\n")
	}
}

func (g *Graphviz) visitCoalescence(c *Coalescence) {

	g.WriteString(fmt.Sprintf(`"%s" [label="Coalescence"];`, c.ID))
	// TODO: show only defaultMaxDepth = 4 nodes
	for _, s := range c.shards {
		g.WriteString(fmt.Sprintf(`"%s" -> "%s";`, s.GetID(), c.GetID()))
		g.WriteString("\n")
	}
}

func (g *Graphviz) visitBinary(b *Binary) {
	g.WriteString(fmt.Sprintf(`"%s" [label="Binary:%s"];`, b.GetID(), b.Kind))
	g.WriteString("\n")
	if b.lhs != nil {
		g.WriteString(fmt.Sprintf(`"%s" -> "%s";`, b.lhs.GetID(), b.GetID()))
		g.WriteString("\n")
	}
	if b.rhs != nil {
		g.WriteString(fmt.Sprintf(`"%s" -> "%s";`, b.rhs.GetID(), b.GetID()))
		g.WriteString("\n")
	}
}

func (g *Graphviz) visitFilter(f *Filter) {
	details := ""
	if f.Kind == "line" {
		details = fmt.Sprintf(`%s\"%s\"`, f.ty, f.match)
	}
	g.WriteString(fmt.Sprintf(`"%s" [label="Filter:%s\n%s"];`, f.GetID(), f.Kind, details))
	g.WriteString("\n")
	if f.Child() != nil {
		g.WriteString(fmt.Sprintf(`"%s" -> "%s";`, f.Child().GetID(), f.GetID()))
		g.WriteString("\n")
	}
}

func (g *Graphviz) visitMap(m *Map) {
	g.WriteString(fmt.Sprintf(`"%s" [label="Map:%s"];`, m.GetID(), m.Kind))
	g.WriteString("\n")
	if m.Child() != nil {
		g.WriteString(fmt.Sprintf(`"%s" -> "%s";`, m.Child().GetID(), m.GetID()))
		g.WriteString("\n")
	}
}

func (g *Graphviz) visitScan(s *Scan) {
	labels := strconv.Quote(s.Labels())
	g.WriteString(fmt.Sprintf(`"%s" [label="Scan\nLabels: %s`, s.GetID(), labels[1:len(labels)-1]))
	if s.shard != nil {
		g.WriteString(fmt.Sprintf("\nShard %d of %d", s.shard.Shard, s.shard.Of))
	}
	g.WriteString(`"];`)
	g.WriteString("\n")
	if s.Child() != nil {
		g.WriteString(fmt.Sprintf(`"%s" -> "%s";`, s.Child().GetID(), s.GetID()))
		g.WriteString("\n")
	}
}
