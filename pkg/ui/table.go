package ui

import (
	"fmt"
	"strings"
)

// Alignment defines cell content alignment.
type Alignment int

const (
	AlignLeft Alignment = iota
	AlignRight
	AlignCenter
)

// Table represents a monospace brutalist terminal table.
type Table struct {
	Title      string
	Headers    []string
	Rows       [][]string
	Alignments []Alignment
}

// NewTable initializes a new brutalist Table.
func NewTable(title string, headers ...string) *Table {
	alignments := make([]Alignment, len(headers))
	for i := range alignments {
		alignments[i] = AlignLeft
	}
	return &Table{
		Title:      title,
		Headers:    headers,
		Rows:       make([][]string, 0),
		Alignments: alignments,
	}
}

// SetAlignment sets column alignments.
func (t *Table) SetAlignment(alignments ...Alignment) {
	t.Alignments = alignments
}

// AddRow adds a row to the table.
func (t *Table) AddRow(cells ...string) {
	t.Rows = append(t.Rows, cells)
}

// Render returns the monospace formatted table string.
func (t *Table) Render() string {
	numCols := len(t.Headers)
	if numCols == 0 {
		return ""
	}

	widths := make([]int, numCols)
	for i, h := range t.Headers {
		if len(h) > widths[i] {
			widths[i] = len(h)
		}
	}

	for _, row := range t.Rows {
		for i, cell := range row {
			if i < numCols && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	totalInnerWidth := 0
	for _, w := range widths {
		totalInnerWidth += w + 2 // 1 space padding on each side
	}
	totalInnerWidth += numCols - 1 // separators

	if len(t.Title) > totalInnerWidth {
		extra := len(t.Title) - totalInnerWidth
		widths[numCols-1] += extra
		totalInnerWidth = len(t.Title)
	}

	var sb strings.Builder

	border := t.horizontalBorder(widths, "+", "-")

	if t.Title != "" {
		fullBorder := "+" + strings.Repeat("-", totalInnerWidth+2) + "+"
		sb.WriteString(fullBorder + "\n")
		titlePadded := fmt.Sprintf("| %-*s |", totalInnerWidth, t.Title)
		sb.WriteString(titlePadded + "\n")
	}

	sb.WriteString(border + "\n")

	// Header
	sb.WriteString("|")
	for i, h := range t.Headers {
		align := AlignLeft
		if i < len(t.Alignments) {
			align = t.Alignments[i]
		}
		sb.WriteString(" " + formatCell(h, widths[i], align) + " |")
	}
	sb.WriteString("\n")

	sb.WriteString(border + "\n")

	// Rows
	if len(t.Rows) == 0 {
		emptyMsg := "No entries found."
		sb.WriteString(fmt.Sprintf("| %-*s |\n", totalInnerWidth, emptyMsg))
	} else {
		for _, row := range t.Rows {
			sb.WriteString("|")
			for i := 0; i < numCols; i++ {
				cellVal := ""
				if i < len(row) {
					cellVal = row[i]
				}
				align := AlignLeft
				if i < len(t.Alignments) {
					align = t.Alignments[i]
				}
				sb.WriteString(" " + formatCell(cellVal, widths[i], align) + " |")
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString(border)
	return sb.String()
}

func (t *Table) horizontalBorder(widths []int, corner, fill string) string {
	var sb strings.Builder
	sb.WriteString(corner)
	for _, w := range widths {
		sb.WriteString(strings.Repeat(fill, w+2))
		sb.WriteString(corner)
	}
	return sb.String()
}

func formatCell(val string, width int, align Alignment) string {
	if len(val) >= width {
		return val
	}
	pad := width - len(val)
	switch align {
	case AlignRight:
		return strings.Repeat(" ", pad) + val
	case AlignCenter:
		left := pad / 2
		right := pad - left
		return strings.Repeat(" ", left) + val + strings.Repeat(" ", right)
	default:
		return val + strings.Repeat(" ", pad)
	}
}

// Banner renders a brutalist key-value status block.
func Banner(title string, items [][2]string) string {
	maxKeyLen := 0
	for _, item := range items {
		if len(item[0]) > maxKeyLen {
			maxKeyLen = len(item[0])
		}
	}

	totalWidth := 68
	for _, item := range items {
		lineLen := maxKeyLen + len(item[1]) + 7
		if lineLen > totalWidth {
			totalWidth = lineLen
		}
	}

	var sb strings.Builder
	lineBorder := strings.Repeat("=", totalWidth)

	sb.WriteString(lineBorder + "\n")
	sb.WriteString(fmt.Sprintf(" %s\n", strings.ToUpper(title)))
	sb.WriteString(lineBorder + "\n")

	for _, item := range items {
		sb.WriteString(fmt.Sprintf(" %-*s : %s\n", maxKeyLen, item[0], item[1]))
	}

	sb.WriteString(lineBorder)
	return sb.String()
}

// AlertBox renders a high-visibility terminal alert card.
func AlertBox(level string, message string, details ...string) string {
	totalWidth := 68
	if len(message)+4 > totalWidth {
		totalWidth = len(message) + 4
	}
	for _, d := range details {
		if len(d)+4 > totalWidth {
			totalWidth = len(d) + 4
		}
	}

	var sb strings.Builder
	border := "+" + strings.Repeat("-", totalWidth-2) + "+"

	sb.WriteString(border + "\n")
	sb.WriteString(fmt.Sprintf("| [%s] %-*s |\n", strings.ToUpper(level), totalWidth-len(level)-6, message))
	if len(details) > 0 {
		sb.WriteString("|" + strings.Repeat(" ", totalWidth-2) + "|\n")
		for _, d := range details {
			sb.WriteString(fmt.Sprintf("|   %-*s |\n", totalWidth-6, d))
		}
	}
	sb.WriteString(border)
	return sb.String()
}

// FormatBytes formats byte counts for monospace output.
func FormatBytes(b int64) string {
	if b < 0 {
		return "0 B"
	}
	const (
		unitKB = 1024
		unitMB = 1024 * unitKB
		unitGB = 1024 * unitMB
		unitTB = 1024 * unitGB
	)

	switch {
	case b >= unitTB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(unitTB))
	case b >= unitGB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(unitGB))
	case b >= unitMB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(unitMB))
	case b >= unitKB:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(unitKB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
