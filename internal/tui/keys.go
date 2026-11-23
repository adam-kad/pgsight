package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Move                                    key.Binding // help text only
	Up, Down, PageUp, PageDown, Top, Bottom key.Binding
	Collapse, Expand, Toggle                key.Binding
	Details, NextHint, Help, Close, Quit    key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Move:     key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "move")),
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page down")),
		Top:      key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g/home", "top")),
		Bottom:   key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G/end", "bottom")),
		Collapse: key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "fold/parent")),
		Expand:   key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "unfold/child")),
		Toggle:   key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "fold")),
		Details:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "details")),
		NextHint: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next hint")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Close:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close panel")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Move, k.Collapse, k.Expand, k.Details, k.NextHint, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		{k.Collapse, k.Expand, k.Toggle},
		{k.Details, k.NextHint, k.Close, k.Help, k.Quit},
	}
}
