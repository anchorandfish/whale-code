package tui

import (
	"github.com/anchorandfish/whale-code/internal/keybindings"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
)

// Update handles a message and returns a model update and command (stub for non-TUI modes)
func (u *UIModel) Update(msg interface{}) (interface{}, interface{}) {
	return nil, nil
}

// View renders the current view (stub for non-TUI modes)
func (u *UIModel) View() string {
	return ""
}

// Width returns the current terminal width
func (u *UIModel) Width() int {
	return u.width
}

// Height returns the current terminal height
func (u *UIModel) Height() int {
	return u.height
}

// SetWidth sets the terminal width
func (u *UIModel) SetWidth(w int) {
	u.width = w
}

// SetHeight sets the terminal height
func (u *UIModel) SetHeight(h int) {
	u.height = h
}

// Viewport returns the viewport model
func (u *UIModel) Viewport() *viewport.Model {
	return &u.viewport
}

// Textarea returns the textarea model
func (u *UIModel) Textarea() *textarea.Model {
	return &u.textarea
}

// SpinnerIdx returns the spinner animation index
func (u *UIModel) SpinnerIdx() int {
	return u.spinnerIdx
}

// SetSpinnerIdx sets the spinner animation index
func (u *UIModel) SetSpinnerIdx(idx int) {
	u.spinnerIdx = idx
}

// Ready returns whether the UI is ready
func (u *UIModel) Ready() bool {
	return u.ready
}

// SetReady sets the ready flag
func (u *UIModel) SetReady(ready bool) {
	u.ready = ready
}

// Dirty returns whether the display needs refresh
func (u *UIModel) Dirty() bool {
	return u.dirty
}

// SetDirty sets the dirty flag
func (u *UIModel) SetDirty(dirty bool) {
	u.dirty = dirty
}

// XOffset returns the horizontal offset
func (u *UIModel) XOffset() int {
	return u.xOffset
}

// SetXOffset sets the horizontal offset
func (u *UIModel) SetXOffset(offset int) {
	u.xOffset = offset
}

// InputFocusedOnSend returns whether input is focused on send
func (u *UIModel) InputFocusedOnSend() bool {
	return u.inputFocusedOnSend
}

// SetInputFocusedOnSend sets the send focus flag
func (u *UIModel) SetInputFocusedOnSend(focused bool) {
	u.inputFocusedOnSend = focused
}

// InputFocusedOnStop returns whether input is focused on stop
func (u *UIModel) InputFocusedOnStop() bool {
	return u.inputFocusedOnStop
}

// SetInputFocusedOnStop sets the stop focus flag
func (u *UIModel) SetInputFocusedOnStop(focused bool) {
	u.inputFocusedOnStop = focused
}

// VimMode returns whether vim mode is enabled
func (u *UIModel) VimMode() bool {
	return u.vimMode
}

// SetVimMode sets the vim mode flag
func (u *UIModel) SetVimMode(enabled bool) {
	u.vimMode = enabled
}

// VimModeState returns the current vim mode state
func (u *UIModel) VimModeState() VimModeState {
	return u.vimModeState
}

// SetVimModeState sets the vim mode state
func (u *UIModel) SetVimModeState(state VimModeState) {
	u.vimModeState = state
}

// VimPendingKey returns the pending vim key
func (u *UIModel) VimPendingKey() string {
	return u.vimPendingKey
}

// SetVimPendingKey sets the pending vim key
func (u *UIModel) SetVimPendingKey(key string) {
	u.vimPendingKey = key
}

// Keybindings returns the keybindings manager
func (u *UIModel) Keybindings() *keybindings.Manager {
	return u.keybindings
}

// MDRenderer returns the markdown renderer
func (u *UIModel) MDRenderer() *MarkdownRenderer {
	return u.mdRenderer
}

// ImagePasteHandler returns the image paste handler
func (u *UIModel) ImagePasteHandler() *ImagePasteHandler {
	return u.imagePasteHandler
}

// PendingImages returns the pending images
func (u *UIModel) PendingImages() []PendingImage {
	return u.pendingImages
}

// SetPendingImages sets the pending images
func (u *UIModel) SetPendingImages(images []PendingImage) {
	u.pendingImages = images
}

// DocLinks returns the document links
func (u *UIModel) DocLinks() []FileLink {
	return u.docLinks
}

// SetDocLinks sets the document links
func (u *UIModel) SetDocLinks(links []FileLink) {
	u.docLinks = links
}

// DocLinkIdx returns the document link index
func (u *UIModel) DocLinkIdx() int {
	return u.docLinkIdx
}

// SetDocLinkIdx sets the document link index
func (u *UIModel) SetDocLinkIdx(idx int) {
	u.docLinkIdx = idx
}
