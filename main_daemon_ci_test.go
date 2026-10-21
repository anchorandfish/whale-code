package main

import (
	"strings"
	"testing"
)

func TestReadCIPromptBounded(t *testing.T) {
	prompt, err := readCIPrompt(strings.NewReader("private prompt"))
	if err != nil || prompt != "private prompt" {
		t.Fatalf("readCIPrompt = (%q, %v)", prompt, err)
	}
	if _, err := readCIPrompt(strings.NewReader(strings.Repeat("x", maxCIPromptSize+1))); err == nil {
		t.Fatal("oversized stdin prompt was accepted")
	}
	if _, err := readCIPrompt(strings.NewReader("  \n")); err == nil {
		t.Fatal("empty stdin prompt was accepted")
	}
}
