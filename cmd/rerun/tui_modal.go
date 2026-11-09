package main

// Every centered modal shares one box size. Each value is here, with its reason.
const (
	// modalWidthPct keeps a modal clearly smaller than a normal terminal.
	modalWidthPct = 80
	// modalMinWidth keeps a modal wide enough for its rows on a narrow terminal.
	modalMinWidth = 80
	// modalMaxWidth keeps a modal's lines readable on a very wide terminal.
	modalMaxWidth = 120
	// modalHeightPct gives a modal a fixed box, so it never jumps as its content changes.
	modalHeightPct = 80
	// modalMinHeight keeps a header, a rule, a footer and some body rows on a short terminal.
	modalMinHeight = 10
)

// modalSize is the outer box every centered modal draws, never larger than the terminal.
func (m *tuiModel) modalSize() (int, int) {
	w := max(m.width*modalWidthPct/100, min(modalMinWidth, m.width))
	w = min(min(w, modalMaxWidth), m.width)
	h := max(m.height*modalHeightPct/100, min(modalMinHeight, m.height))
	h = min(h, m.height)
	return max(1, w), max(1, h)
}
