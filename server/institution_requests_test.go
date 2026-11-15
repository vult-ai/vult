package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/bankconnect"
	"github.com/vult-ai/vult/bankconnect/banktest"
	"github.com/vult-ai/vult/internal/secretbox"
	"github.com/vult-ai/vult/ledger"
)

// fakeRequests records what would be sent.
type fakeRequests struct {
	mu     sync.Mutex
	notify string
	sent   []InstitutionRequest
	fail   error
}

func (f *fakeRequests) Notify() string { return f.notify }
func (f *fakeRequests) Send(_ context.Context, r InstitutionRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, r)
	return nil
}

// syntheticDNS resolves only the example banks' domains.
func syntheticDNS(_ context.Context, host string) error {
	if host == "examplebank.com" || host == "www.examplecreditunion.com" {
		return nil
	}
	return errors.New("no such host")
}

// takeRequests turns bank requests on for one test.
func takeRequests(t *testing.T) {
	offerBankRequests = true
	t.Cleanup(func() { offerBankRequests = false })
}

func requestServer(t *testing.T, receiver InstitutionRequests) (*documentServerFixture, *http.Server) {
	t.Helper()
	takeRequests(t)
	f, s, _ := bankServerWith(t, func(o *Options) { o.InstitutionRequests = receiver; o.lookupHost = syntheticDNS })
	return f, s
}

// Only the person's click sends a request: the name they searched, the
// bank's website once it resolves, the country, this Vult's version and
// edition, and an email only when they asked to hear back. A second
// click, or the same bank for the same Book that day, sends nothing more.
func TestOwnerRequestsAMissingBank(t *testing.T) {
	receiver := &fakeRequests{notify: "email"}
	f, s := requestServer(t, receiver)
	book := f.book.ID
	// Searching and listing never send anything.
	for _, path := range []string{bankPopularPath + "?book_id=" + book, bankSearchPath + "?book_id=" + book + "&q=nothing"} {
		w := bankCall(t, s, f.session, http.MethodGet, path, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"requests":{"notify":"email"}`) {
			t.Fatal("requests not offered", w.Body.String())
		}
	}
	if len(receiver.sent) != 0 {
		t.Fatal("sent without a click")
	}
	ask := func(body string) *httptest.ResponseRecorder {
		return bankCall(t, s, f.session, http.MethodPost, institutionRequestsPath, body)
	}
	w := ask(`{"book_id":"` + book + `","request_key":"key-0000000000000001","name":"Example Credit Union","website":"https://www.ExampleCreditUnion.com/about","country":"PL","email":"owner@example.com"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"requested"`) || len(receiver.sent) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	got := receiver.sent[0]
	if got.Name != "Example Credit Union" || got.Domain != "www.examplecreditunion.com" || got.Country != "PL" || got.Email != "" || got.Version == "" || got.Edition != "open" {
		t.Fatal("sent", got)
	}
	// The same click again, or the same bank for this Book today, sends nothing more.
	for _, again := range []string{
		`{"book_id":"` + book + `","request_key":"key-0000000000000001","name":"Example Credit Union","website":"www.examplecreditunion.com"}`,
		`{"book_id":"` + book + `","request_key":"key-0000000000000002","name":"Example  credit union","website":"www.examplecreditunion.com"}`,
	} {
		if w := ask(again); w.Code != http.StatusOK || len(receiver.sent) != 1 {
			t.Fatal("sent twice", w.Code, len(receiver.sent))
		}
	}
	// The website must be a domain that resolves.
	for _, site := range []string{"", "not a site", "localhost", "192.0.2.1", "examplebank", "https://user@examplebank.com", "examplebank.com:8443", "missing-example-bank.com"} {
		if w := ask(`{"book_id":"` + book + `","request_key":"key-0000000000000003","name":"Example Bank","website":"` + site + `"}`); w.Code != http.StatusUnprocessableEntity || len(receiver.sent) != 1 {
			t.Fatalf("website %q: %d %s", site, w.Code, w.Body.String())
		}
	}
	// Hearing back needs an email, and only then is one sent.
	if w := ask(`{"book_id":"` + book + `","request_key":"key-0000000000000004","name":"Example Bank","website":"examplebank.com","notify":true}`); w.Code != http.StatusUnprocessableEntity || len(receiver.sent) != 1 {
		t.Fatal("asked to hear back without an email", w.Code)
	}
	if w := ask(`{"book_id":"` + book + `","request_key":"key-0000000000000004","name":"Example Bank","website":"examplebank.com","notify":true,"email":"owner@example.com"}`); w.Code != http.StatusOK || len(receiver.sent) != 2 || receiver.sent[1].Email != "owner@example.com" {
		t.Fatal("the email was not sent", w.Code, receiver.sent)
	}
	for _, bad := range []string{
		`{"book_id":"` + book + `","request_key":"short","name":"Example","website":"examplebank.com"}`,
		`{"book_id":"` + book + `","request_key":"key-0000000000000005","name":"","website":"examplebank.com"}`,
		`{"book_id":"` + book + `","request_key":"key-0000000000000005","name":"Example","website":"examplebank.com","country":"USA"}`,
		`{"book_id":"` + book + `","request_key":"key-0000000000000005","name":"Example","website":"examplebank.com","country":"gb"}`,
		`{"book_id":"` + book + `","request_key":"key-0000000000000005","name":"Example","website":"examplebank.com","institution_id":"ins_1"}`,
		`{"book_id":"` + ledger.ID() + `","request_key":"key-0000000000000005","name":"Example","website":"examplebank.com"}`,
	} {
		if w := ask(bad); w.Code == http.StatusOK {
			t.Fatalf("accepted %s", bad)
		}
	}
	// A refusal says why; an unreachable receiver is 503, and the request
	// can be sent again afterwards.
	third := `{"book_id":"` + book + `","request_key":"key-0000000000000006","name":"Third Example Bank","website":"examplebank.com"}`
	receiver.fail = &ledger.RefusedError{Reason: "Too many requests today."}
	if w := ask(third); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Too many requests today.") {
		t.Fatal(w.Code, w.Body.String())
	}
	receiver.fail = errRequestsUnavailable
	if w := ask(third); w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code, w.Body.String())
	}
	receiver.fail = nil
	if w := ask(third); w.Code != http.StatusOK || len(receiver.sent) != 3 {
		t.Fatal("not sent after the receiver came back", w.Code)
	}
}

// A receiver that knows the account is told who asked, and no typed email.
func TestAccountReceiverNeedsNoEmail(t *testing.T) {
	receiver := &fakeRequests{notify: "account"}
	f, s := requestServer(t, receiver)
	w := bankCall(t, s, f.session, http.MethodPost, institutionRequestsPath, `{"book_id":"`+f.book.ID+`","request_key":"key-0000000000000001","name":"Example Bank","website":"examplebank.com","notify":true,"email":"other@example.com"}`)
	if w.Code != http.StatusOK || len(receiver.sent) != 1 || receiver.sent[0].Email != "" || receiver.sent[0].Subject == "" || !receiver.sent[0].Notify {
		t.Fatal(w.Code, receiver.sent)
	}
}

// requestServerConfig is bankServer with a configuration a test changes.
func requestServerConfig(t *testing.T, change func(*Config)) (*documentServerFixture, *http.Server) {
	t.Helper()
	takeRequests(t)
	f := documentServer(t)
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	banks := &bankconnect.Service{Store: ledger.Store{Runtime: f.db.Runtime}, Connectors: map[string]bankconnect.Connector{screenKey: &banktest.Screen{Key: screenKey}},
		Keys: &secretbox.Cached{Source: secretbox.FileSource{Path: filepath.Join(t.TempDir(), "bank-keys.json")}}}
	c := configuration()
	change(&c)
	s, err := New(c, Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login},
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}}, Banks: banks, BankScreen: exampleScreen(), Documents: f.vault, lookupHost: syntheticDNS})
	if err != nil {
		t.Fatal(err)
	}
	return f, s
}

// Every Vult offers requests to Vult by default, with the option to hear
// back by email; "off" offers nothing and sends nothing.
func TestRequestsCanBeTurnedOff(t *testing.T) {
	// Where Vult takes requests, they're on unless configured off.
	takeRequests(t)
	f, s, _ := bankServer(t)
	w := bankCall(t, s, f.session, http.MethodGet, bankPopularPath+"?book_id="+f.book.ID, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"requests":{"notify":"email"}`) {
		t.Fatal("requests are not on by default", w.Body.String())
	}
	f, s = requestServerConfig(t, func(c *Config) { c.InstitutionRequests = "off" })
	w = bankCall(t, s, f.session, http.MethodGet, bankPopularPath+"?book_id="+f.book.ID, "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"requests"`) {
		t.Fatal(w.Body.String())
	}
	if w := bankCall(t, s, f.session, http.MethodPost, institutionRequestsPath, `{"book_id":"`+f.book.ID+`","request_key":"key-0000000000000001","name":"Example Bank","website":"examplebank.com"}`); w.Code != http.StatusNotFound {
		t.Fatal(w.Code)
	}
}

// A receiver asking this Vult to wait is passed on with its wait, and an
// unreachable one says so plainly.
func TestRequestLimitsAndOutagesAreRetryable(t *testing.T) {
	receiver := &fakeRequests{notify: "none", fail: limitedError{retryAfter: "3600"}}
	f, s := requestServer(t, receiver)
	body := `{"book_id":"` + f.book.ID + `","request_key":"key-0000000000000001","name":"Example Bank","website":"examplebank.com","notify":true,"email":"owner@example.com"}`
	w := bankCall(t, s, f.session, http.MethodPost, institutionRequestsPath, body)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "3600" || !strings.Contains(w.Body.String(), `"error":"limited"`) {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	receiver.fail = errRequestsUnavailable
	w = bankCall(t, s, f.session, http.MethodPost, institutionRequestsPath, body)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Try again") {
		t.Fatal(w.Code, w.Body.String())
	}
	receiver.fail = nil
	if w := bankCall(t, s, f.session, http.MethodPost, institutionRequestsPath, body); w.Code != http.StatusOK || len(receiver.sent) != 1 || receiver.sent[0].Email != "" || receiver.sent[0].Notify {
		t.Fatal("an email went to a receiver that takes none", w.Code, receiver.sent)
	}
}

func TestRequestsReceiverChoice(t *testing.T) {
	given := &fakeRequests{notify: "account"}
	if r, err := chooseRequests("off", given, ""); err != nil || r != nil {
		t.Fatal("off kept a receiver", r, err)
	}
	if r, err := chooseRequests("", nil, "0123456789abcdef0123456789abcdef"); err != nil || r.(forwardedRequests).url != "https://vult.ai/api/v1/bank-requests" || r.Notify() != "email" {
		t.Fatal("Vult's own address was not the default", r, err)
	}
	if r, err := chooseRequests("", nil, ""); err != nil || !vultID.MatchString(r.(forwardedRequests).id) {
		t.Fatal("no vult_id for this run", r, err)
	}
	if _, err := chooseRequests("", nil, "short"); err == nil {
		t.Fatal("a malformed vult_id was accepted")
	}
	if r, err := chooseRequests("", given, ""); err != nil || r != given {
		t.Fatal("the host's receiver was not used", r, err)
	}
	if r, err := chooseRequests("https://requests.example.com/banks", nil, ""); err != nil || r.(forwardedRequests).url != "https://requests.example.com/banks" {
		t.Fatal("the configured address was not used", r, err)
	}
	for _, bad := range []string{"http://requests.example.com", "https://user@requests.example.com", "https://requests.example.com/?x=1", "requests.example.com"} {
		if _, err := chooseRequests(bad, nil, ""); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}

// The forwarder sends exactly what Vult's receiver takes, and reads its
// answers: recorded or already counted, a refusal in plain words, a wait,
// or unavailable.
func TestForwardedRequests(t *testing.T) {
	var got map[string]any
	status, reply, after := http.StatusCreated, `{"schema_version":1,"status":"recorded"}`, ""
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = nil
		json.Unmarshal(body, &got)
		if after != "" {
			w.Header().Set("Retry-After", after)
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	f := forwardedRequests{url: receiver.URL, id: "0123456789abcdef0123456789abcdef", client: receiver.Client()}
	r := InstitutionRequest{Key: "key-0000000000000001", Name: "Example Bank", Domain: "examplebank.com", Country: "GB", Version: "0.8.0", Edition: "open",
		Email: "owner@example.com", Notify: true, Subject: "owner-subject"}
	if err := f.Send(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	// Exactly these fields leave the machine: no request key or subject, and
	// the email only when the person asked to hear back.
	exactly := func(want map[string]any) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatal("sent", got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatal("sent", got)
			}
		}
	}
	exactly(map[string]any{"name": "Example Bank", "domain": "examplebank.com", "country": "GB", "vult_version": "0.8.0", "edition": "open", "vult_id": f.id, "notify_email": "owner@example.com"})
	r.Notify = false
	if err := f.Send(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	exactly(map[string]any{"name": "Example Bank", "domain": "examplebank.com", "country": "GB", "vult_version": "0.8.0", "edition": "open", "vult_id": f.id})
	r.Country = ""
	status, reply = http.StatusOK, `{"schema_version":1,"status":"already"}`
	if err := f.Send(context.Background(), r); err != nil || got["country"] != nil {
		t.Fatal("already counted", err, got)
	}
	status, reply = http.StatusBadRequest, `{"schema_version":1,"error":"invalid","field":"domain","message":"examplebank.com isn't a website we can find."}`
	if err := f.Send(context.Background(), r); err == nil || err.Error() != "examplebank.com isn't a website we can find." {
		t.Fatal(err)
	}
	status, reply, after = http.StatusTooManyRequests, `{"schema_version":1,"error":"limited"}`, "1800"
	var limited limitedError
	if err := f.Send(context.Background(), r); !errors.As(err, &limited) || limited.retryAfter != "1800" {
		t.Fatal(err)
	}
	status, reply, after = http.StatusServiceUnavailable, `{"schema_version":1,"error":"unavailable"}`, ""
	if err := f.Send(context.Background(), r); err != errRequestsUnavailable {
		t.Fatal(err)
	}
	receiver.Close()
	if err := f.Send(context.Background(), r); err != errRequestsUnavailable {
		t.Fatal("an unreachable receiver", err)
	}
}

// Requests are off in this release: even with a receiver given, the picker
// isn't told about them and the request route isn't there, so nothing is
// ever sent.
func TestRequestsAreOffByDefault(t *testing.T) {
	receiver := &fakeRequests{notify: "email"}
	f, s, _ := bankServerWith(t, func(o *Options) { o.InstitutionRequests = receiver; o.lookupHost = syntheticDNS })
	book := f.book.ID
	if w := bankCall(t, s, f.session, http.MethodGet, bankPopularPath+"?book_id="+book, ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"requests"`) {
		t.Fatal("the picker was told about requests", w.Code, w.Body.String())
	}
	w := bankCall(t, s, f.session, http.MethodPost, institutionRequestsPath, `{"book_id":"`+book+`","request_key":"0123456789abcdef0123456789abcdef","name":"Example Neobank","website":"example-neobank.com"}`)
	if w.Code != http.StatusNotFound || len(receiver.sent) != 0 {
		t.Fatal("a request was taken", w.Code, len(receiver.sent))
	}
}
