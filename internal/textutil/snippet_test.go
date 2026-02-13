package textutil

import "testing"

func TestCleanSnippet_RemovesMarkdownNoise(t *testing.T) {
	in := "# Title\n\nSome **bold** text with [link](https://example.com).\n\n\nNext."
	out := CleanSnippet(in, 0)

	want := "Title\n\nSome bold text with link.\n\nNext."
	if out != want {
		t.Fatalf("unexpected cleaned snippet:\nwant: %q\ngot:  %q", want, out)
	}
}

func TestCleanSnippet_TruncatesByRunes(t *testing.T) {
	in := "你好世界abc"
	out := CleanSnippet(in, 4)
	want := "你..."
	if out != want {
		t.Fatalf("unexpected rune truncation:\nwant: %q\ngot:  %q", want, out)
	}
}

func TestCleanSnippet_TruncatesAtSentenceBoundary(t *testing.T) {
	in := "第一句。第二句很长很长很长。第三句"
	out := CleanSnippet(in, 16)
	want := "第一句。..."
	if out != want {
		t.Fatalf("unexpected sentence-aware truncation:\nwant: %q\ngot:  %q", want, out)
	}
}
