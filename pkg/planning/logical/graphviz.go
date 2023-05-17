package logical

import (
	"fmt"
	"io"
	"strconv"
)

type Graphviz struct {
	writer io.StringWriter
}

func (g *Graphviz) visitAggregation(a *Aggregation) {
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Aggregation:%s\n%s"];`, a.GetID(), a.Details.Name(), a.Details.Parameters()))
	g.writer.WriteString("\n")
	if a.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, a.Child().GetID(), a.GetID()))
		g.writer.WriteString("\n")
	}
}

func (g *Graphviz) visitBinary(b *Binary) {
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Binary:%s"];`, b.GetID(), b.Kind))
	g.writer.WriteString("\n")
	if b.lhs != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, b.lhs.GetID(), b.GetID()))
		g.writer.WriteString("\n")
	}
	if b.rhs != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, b.rhs.GetID(), b.GetID()))
		g.writer.WriteString("\n")
	}
}

func (g *Graphviz) visitFilter(f *Filter) {
	details := ""
	if f.Kind == "line" {
		details = fmt.Sprintf(`%s\"%s\"`, f.ty, f.match)
	}
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Filter:%s\n%s"];`, f.GetID(), f.Kind, details))
	g.writer.WriteString("\n")
	if f.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, f.Child().GetID(), f.GetID()))
		g.writer.WriteString("\n")
	}
}

func (g *Graphviz) visitMap(m *Map) {
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Map:%s"];`, m.GetID(), m.Kind))
	g.writer.WriteString("\n")
	if m.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, m.Child().GetID(), m.GetID()))
		g.writer.WriteString("\n")
	}
}

func (g *Graphviz) visitScan(s *Scan) {
	labels := strconv.Quote(s.Labels())
	g.writer.WriteString(fmt.Sprintf(`"%s" [label="Scan\nLabels: %s"];`, s.GetID(), labels[1:len(labels)-1]))
	g.writer.WriteString("\n")
	if s.Child() != nil {
		g.writer.WriteString(fmt.Sprintf(`"%s" -> "%s";`, s.Child().GetID(), s.GetID()))
		g.writer.WriteString("\n")
	}
}
