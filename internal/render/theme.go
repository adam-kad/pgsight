package render

import (
	"io"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Heat thresholds for the share of total time a node takes by itself.
const (
	WarmPercent = 10 // from here on: yellow
	HotPercent  = 30 // above this: red
)

// Theme holds the styles used by both the plain renderer and the TUI.
type Theme struct {
	r *lipgloss.Renderer

	Title    lipgloss.Style // node type
	Target   lipgloss.Style // "on orders o"
	Subplan  lipgloss.Style // "CTE big:", "SubPlan 1:"
	Dim      lipgloss.Style // tree guides, secondary text
	Header   lipgloss.Style
	Key      lipgloss.Style // keys in the details panel
	Warning  lipgloss.Style
	Info     lipgloss.Style
	Cool     lipgloss.Style
	Warm     lipgloss.Style
	Hot      lipgloss.Style
	Selected lipgloss.Style
	Border   lipgloss.Style
}

// NewTheme builds styles for output to w. With color false (or when w is
// not a terminal) everything renders as plain text.
func NewTheme(w io.Writer, color bool) *Theme {
	r := lipgloss.NewRenderer(w)
	if !color {
		r.SetColorProfile(termenv.Ascii)
	}
	return NewThemeWithRenderer(r)
}

// NewThemeWithRenderer builds styles on an existing renderer.
func NewThemeWithRenderer(r *lipgloss.Renderer) *Theme {
	s := r.NewStyle
	return &Theme{
		r:        r,
		Title:    s().Bold(true),
		Target:   s(),
		Subplan:  s().Foreground(lipgloss.Color("6")).Italic(true),
		Dim:      s().Foreground(lipgloss.Color("8")),
		Header:   s().Bold(true),
		Key:      s().Foreground(lipgloss.Color("6")),
		Warning:  s().Foreground(lipgloss.Color("3")).Bold(true),
		Info:     s().Foreground(lipgloss.Color("4")).Bold(true),
		Cool:     s().Foreground(lipgloss.Color("2")),
		Warm:     s().Foreground(lipgloss.Color("3")),
		Hot:      s().Foreground(lipgloss.Color("1")).Bold(true),
		Selected: s().Reverse(true),
		Border:   s().Foreground(lipgloss.Color("8")),
	}
}

// Renderer returns the lipgloss renderer behind the theme.
func (t *Theme) Renderer() *lipgloss.Renderer { return t.r }

// Heat picks the color for a node's share of the total:
// green below 10%, yellow from 10% to 30%, red above 30%.
func (t *Theme) Heat(pct float64) lipgloss.Style {
	switch {
	case pct > HotPercent:
		return t.Hot
	case pct >= WarmPercent:
		return t.Warm
	default:
		return t.Cool
	}
}
