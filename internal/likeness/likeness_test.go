package likeness

import "testing"

func TestNames(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want Verdict
	}{
		{"Łucja Wójcik-Nowak", "LUCJA WOJCIK-NOWAK", Same},
		{"Zoë Brontë", "Zoe Bronte", Same},
		{"KOWALSKI JAN", "Jan Kowalski", Same},
		{"Eightball, Inc.", "EIGHTBALL INC", Same},
		{"Kit Blanche", "Kit R. Blanche", Extra},
		{"K. Blanche", "Kit Blanche", Extra},
		{"Kit Blanche", "Kit Blanchard", Unsure},
		{"Kit Blanche", "Eightball Inc", Different},
		{"Blanche", "Kit Blanche", Unsure},
		{"Lucia Woycik", "Łucja Wójcik", Unsure},
		{"Kit Blanche", "Kat Blinch", Different},
		{"Spółka Przykład sp. z o.o.", "SPOLKA PRZYKLAD SP Z OO", Same},
	} {
		if got := Names(c.a, c.b).Verdict; got != c.want {
			t.Errorf("Names(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAddresses(t *testing.T) {
	for _, c := range []struct {
		found, expected string
		want            Verdict
	}{
		{"RYNEK 50/7, 31-001 KRAKÓW", "Rynek 50, 31001 Kraków", Extra},
		{"Rynek 50, 31-001 Krakow", "Rynek 50, 31-001 Kraków", Same},
		{"ul. Świętojańska 12, 81-372 Gdynia", "Ulica Swietojanska 12, 81-372 Gdynia", Same},
		{"100 Main Street Apt 4B, Springfield", "100 Main St #4B, Springfield", Same},
		{"100 Main St, Springfield", "100 Main St Unit 4, Springfield", Extra},
		{"100 Main St Unit 3, Springfield", "100 Main St Unit 4, Springfield", Different},
		{"12 Baker Street, London SW1A 1AA", "12 Baker St, London SW1A1AA", Same},
		{"101 Main St, Springfield", "100 Main St, Springfield", Unsure},
		{"9 Rue Lepic, Paris", "100 Main St, Springfield", Different},
	} {
		got := Addresses(c.found, c.expected)
		if got.Verdict != c.want {
			t.Errorf("Addresses(%q, %q) = %v, want %v", c.found, c.expected, got.Verdict, c.want)
		}
		if got.Verdict == Extra && got.Note == "" {
			t.Errorf("an extra says what it is: %q", c.found)
		}
	}
	if n := Addresses("RYNEK 50/7, 31-001 KRAKÓW", "Rynek 50, 31001 Kraków").Note; n != "The statement shows RYNEK 50/7, 31-001 KRAKÓW; the Book expects no flat or unit number." {
		t.Error("the note quotes the statement as written", n)
	}
}
