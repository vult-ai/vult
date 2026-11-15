package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/bankconnect"
	"github.com/vult-ai/vult/bankconnect/banktest"
	"github.com/vult-ai/vult/ledger"
)

type pickerAnswer struct {
	Institutions []pickerBank `json:"institutions"`
}

// The owner's bank picker lists popular banks in a fixed order and finds
// others by name, with logos and colors, from the screen provider's
// directory; answers are kept, and the provider's screen opens at the bank
// the owner picked. When the directory doesn't answer, the popular names
// still let the owner pick.
func TestOwnerPicksABankBeforeTheProvidersScreen(t *testing.T) {
	f, s, banks := bankServer(t)
	fake := screenOf(t, banks)
	book := f.book.ID
	read := func(path string) pickerAnswer {
		t.Helper()
		w := bankCall(t, s, f.session, http.MethodGet, path, "")
		var out pickerAnswer
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || strings.Contains(w.Body.String(), `"needs"`) {
			t.Fatal(path, w.Code, w.Body.String())
		}
		return out
	}
	popular := bankPopularPath + "?book_id=" + book
	fake.Fail = map[string]error{"search": bankconnect.ErrProvider}
	out := read(popular)
	if len(out.Institutions) != len(bankconnect.PopularInstitutions) || out.Institutions[0].Name != "Chase" || out.Institutions[0].ID != "" || out.Institutions[0].Connector != screenKey {
		t.Fatal("popular names while the directory doesn't answer", out)
	}
	if w := bankCall(t, s, f.session, http.MethodGet, bankSearchPath+"?book_id="+book+"&q=chase", ""); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "The bank connection service didn't complete this.") {
		t.Fatal("a search the directory didn't answer", w.Code, w.Body.String())
	}
	fake.Fail = nil
	out = read(popular)
	if len(out.Institutions) != len(bankconnect.PopularInstitutions) || out.Institutions[0].ID != "ins_chase" || out.Institutions[0].Color != "#123456" || out.Institutions[0].Connector != screenKey {
		t.Fatal("popular banks", out)
	}
	before := fake.Searches()
	if read(popular); fake.Searches() != before {
		t.Fatal("the popular list was not kept", fake.Searches()-before)
	}
	out = read(bankSearchPath + "?book_id=" + book + "&q=+Example+++Credit+Union+")
	if len(out.Institutions) != 1 || out.Institutions[0].ID != "ins_example_credit_union" || !strings.HasPrefix(out.Institutions[0].Logo, "data:image/png;base64,") {
		t.Fatal("search", out)
	}
	if out := read(bankSearchPath + "?book_id=" + book + "&q="); len(out.Institutions) != 0 {
		t.Fatal("an empty search listed banks", out)
	}
	for _, bad := range []string{bankSearchPath + "?book_id=" + book + "&q=" + strings.Repeat("a", 81), bankSearchPath + "?book_id=" + book, popular + "&q=x"} {
		if w := bankCall(t, s, f.session, http.MethodGet, bad, ""); w.Code == http.StatusOK {
			t.Fatal("accepted", bad)
		}
	}
	// The screen opens at the bank picked, by its routing number. A
	// malformed routing number, or one with a sign-in again, is refused.
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`","institution_id":"ins_example","routing_number":"011000015"}`); w.Code != http.StatusOK {
		t.Fatal("link start", w.Code, w.Body.String())
	}
	if fake.LastLink().RoutingNumber != "011000015" {
		t.Fatal("the picked bank was not passed on as asked", fake.LastLink())
	}
	for _, bad := range []string{`"routing_number":"011000016"`, `"routing_number":"12345"`, `"routing_number":"011000015","connection_id":"` + book + `"`} {
		if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`",`+bad+`}`); w.Code == http.StatusOK {
			t.Fatalf("accepted %s", bad)
		}
	}
}

// Mercury connects like any other bank: even where Vult's own Mercury
// connector is set up, the grid and the connectors don't offer it and a
// token isn't taken. A Mercury connection made with a token before stays
// listed and can be disconnected.
func TestMercuryConnectsLikeAnyOtherBank(t *testing.T) {
	f, s, _ := bankServerWith(t, func(o *Options) {
		o.Banks.Connectors = map[string]bankconnect.Connector{"example": &banktest.Screen{Key: "example"}, "mercury": &bankconnect.Mercury{}}
		o.BankScreen.Provider = "example"
	})
	book := f.book.ID
	w := bankCall(t, s, f.session, http.MethodGet, bankPopularPath+"?book_id="+book, "")
	var out pickerAnswer
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Institutions) != len(bankconnect.PopularInstitutions) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, b := range out.Institutions {
		if b.Connector != "example" {
			t.Fatal("offered", b)
		}
	}
	if w := bankCall(t, s, f.session, http.MethodGet, bankConnectorsPath+"?book_id="+book, ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "mercury") {
		t.Fatal("connectors", w.Code, w.Body.String())
	}
	w = bankCall(t, s, f.session, http.MethodPost, bankConnectTokenPath, `{"book_id":"`+book+`","provider":"mercury","token":"secret-token:x"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "doesn't take a Mercury token") {
		t.Fatal("a token was taken", w.Code, w.Body.String())
	}
	var login string
	if err := f.sessions.OwnerAct(f.db.Context, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		login, err = o.AddBankConnection(f.db.Context, book, who, ledger.NewBankConnection{Provider: "mercury", Environment: "production", ProviderItemID: "mercury-earlier",
			InstitutionName: "Mercury", TokenSealed: bytes.Repeat([]byte{1}, 48), KeyVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, s, f.session, http.MethodGet, bankPath+"?book_id="+book, ""); !strings.Contains(w.Body.String(), login) {
		t.Fatal("the earlier Mercury connection isn't listed", w.Body.String())
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankDisconnectPath, `{"book_id":"`+book+`","connection_id":"`+login+`"}`); w.Code != http.StatusOK {
		t.Fatal("disconnect", w.Code, w.Body.String())
	}
}

// The picker searches the countries the Vult's Books bank in, from their
// currencies, or the countries the screen names; each set is kept on its
// own, the provider's screen offers the same countries, and the countries
// the provider serves come from the provider.
func TestPickerSearchesTheVultsCountries(t *testing.T) {
	f, s, banks := bankServer(t)
	fake := screenOf(t, banks)
	book := f.book.ID
	var out struct {
		Countries []string `json:"countries"`
	}
	w := bankCall(t, s, f.session, http.MethodGet, bankSearchPath+"?book_id="+book+"&q=revolut", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || strings.Join(out.Countries, ",") != "US" || strings.Join(fake.SearchedCountries(), ",") != "US" {
		t.Fatal("the Book's countries", w.Body.String(), fake.SearchedCountries())
	}
	before := fake.Searches()
	w = bankCall(t, s, f.session, http.MethodGet, bankSearchPath+"?book_id="+book+"&q=revolut&country_codes=gb,IE", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || strings.Join(out.Countries, ",") != "GB,IE" || fake.Searches() != before+1 || len(fake.SearchedCountries()) != 2 {
		t.Fatal("the screen's countries", w.Body.String(), fake.SearchedCountries())
	}
	w = bankCall(t, s, f.session, http.MethodGet, bankPopularPath+"?book_id="+book+"&country=pl", "")
	var popular struct {
		Countries []string `json:"countries"`
		Supported []string `json:"supported_countries"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &popular) != nil || strings.Join(popular.Countries, ",") != "PL" ||
		strings.Join(popular.Supported, ",") != strings.Join(banktest.ScreenCountries, ",") {
		t.Fatal("one country", w.Body.String())
	}
	for _, bad := range []string{"&country_codes=", "&country_codes=USA", "&country_codes=U1", "&country_codes=US&country_codes=GB", "&country=US,GB", "&country=US&country_codes=GB"} {
		if w := bankCall(t, s, f.session, http.MethodGet, bankSearchPath+"?book_id="+book+"&q=revolut"+bad, ""); w.Code == http.StatusOK {
			t.Fatal("accepted", bad)
		}
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`","country_codes":["GB","IE"]}`); w.Code != http.StatusOK {
		t.Fatal("link start", w.Code, w.Body.String())
	}
	if c := fake.LastLink().Countries; strings.Join(c, ",") != "GB,IE" {
		t.Fatal("the screen's countries", c)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`"}`); w.Code != http.StatusOK {
		t.Fatal("link start", w.Code, w.Body.String())
	}
	if c := fake.LastLink().Countries; strings.Join(c, ",") != "US" {
		t.Fatal("the Book's countries", c)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`","country_codes":["USA"]}`); w.Code == http.StatusOK {
		t.Fatal("a malformed country was accepted")
	}
}

func TestCountryCodesAreTwoLetters(t *testing.T) {
	if got, ok := parseCountries(" gb, US ,gb"); !ok || strings.Join(got, ",") != "GB,US" {
		t.Fatal(got, ok)
	}
	for _, bad := range []string{"", "USA", "US,,GB", "U1", "É1"} {
		if _, ok := parseCountries(bad); ok {
			t.Fatal("accepted", bad)
		}
	}
	if !oneCountry("GB") || oneCountry("gb") || oneCountry("GB,US") {
		t.Fatal("one country")
	}
}
