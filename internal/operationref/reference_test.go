package operationref

import (
	"strings"
	"testing"
)

func TestReviewReferenceIsAnExactCanonicalAddress(t *testing.T) {
	const book = "00000000-0000-0000-0000-000000000001"
	const proposal = "ffffffff-ffff-ffff-ffff-fffffffffffe"
	const expected = "AAAAAAAAAAAAAAAAAAAAAf____________________4"
	if Encode(book, proposal) != expected {
		t.Fatal("review address changed its fixed byte layout")
	}
	b, p, ok := Decode(expected)
	if !ok || b != book || p != proposal {
		t.Fatal("review address changed either permanent identity")
	}
	for _, bad := range []string{"", expected + "=", expected[:42], expected[:42] + "5", "https://example.com/", strings.Repeat("!", 43)} {
		if _, _, ok := Decode(bad); ok {
			t.Fatal("invalid or noncanonical review address accepted")
		}
	}
	if Encode("not-a-book", proposal) != "" || Encode(book, strings.ToUpper(proposal)) != "" {
		t.Fatal("invalid identity encoded as a review address")
	}
}
