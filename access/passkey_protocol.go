package access

import (
	"bytes"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/vult-ai/vult/ledger"
)

const passkeyLifetime = 5 * time.Minute
const passkeyResponseLimit = 64 << 10

type passkeyUser struct {
	ID          string
	Name        string
	Credentials []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return []byte(u.ID) }
func (u passkeyUser) WebAuthnName() string                       { return u.Name }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.Name }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

type passkeyProtocol struct {
	web    *webauthn.WebAuthn
	origin string
}

func newPasskeyProtocol(origin string) (*passkeyProtocol, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host != strings.ToLower(u.Host) || strings.ContainsAny(u.Host, "\\% \t\r\n") || net.ParseIP(u.Hostname()) != nil || strings.HasSuffix(u.Host, ":") || strings.HasSuffix(u.Hostname(), ".") {
		return nil, ErrDenied
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p || p == "443" {
			return nil, ErrDenied
		}
	}
	host := u.Hostname()
	if len(host) > 253 || host != "localhost" && !strings.Contains(host, ".") {
		return nil, ErrDenied
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return nil, ErrDenied
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return nil, ErrDenied
			}
		}
	}
	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyLifetime, TimeoutUVD: passkeyLifetime}
	w, err := webauthn.New(&webauthn.Config{RPID: host, RPDisplayName: "Vult", RPOrigins: []string{origin}, RPAllowCrossOrigin: false, AttestationPreference: protocol.PreferNoAttestation, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(), UserVerification: protocol.VerificationRequired}, Timeouts: webauthn.TimeoutsConfig{Login: timeout, Registration: timeout}})
	if err != nil {
		return nil, ErrDenied
	}
	return &passkeyProtocol{web: w, origin: origin}, nil
}

func validPasskeyUser(user passkeyUser) bool {
	return ledger.ValidID(user.ID) && len(user.Name) > 0 && len(user.Name) <= 80 && len(user.Credentials) <= 16
}

func (p *passkeyProtocol) beginRegistration(user passkeyUser) (json.RawMessage, webauthn.SessionData, error) {
	if !validPasskeyUser(user) {
		return nil, webauthn.SessionData{}, ErrDenied
	}
	options, session, err := p.web.BeginRegistration(user, webauthn.WithRegistrationOrigin(p.origin))
	if err != nil {
		return nil, webauthn.SessionData{}, ErrDenied
	}
	data, err := json.Marshal(options)
	if err != nil {
		return nil, webauthn.SessionData{}, ErrDenied
	}
	return data, *session, nil
}

func (p *passkeyProtocol) beginLogin(user passkeyUser) (json.RawMessage, webauthn.SessionData, error) {
	if !validPasskeyUser(user) || len(user.Credentials) == 0 {
		return nil, webauthn.SessionData{}, ErrDenied
	}
	options, session, err := p.web.BeginLogin(user, webauthn.WithLoginOrigin(p.origin))
	if err != nil {
		return nil, webauthn.SessionData{}, ErrDenied
	}
	data, err := json.Marshal(options)
	if err != nil {
		return nil, webauthn.SessionData{}, ErrDenied
	}
	return data, *session, nil
}

func (p *passkeyProtocol) validSession(user passkeyUser, session webauthn.SessionData) bool {
	return validPasskeyUser(user) && bytes.Equal(session.UserID, []byte(user.ID)) && session.Origin == p.origin && session.RelyingPartyID == p.web.Config.RPID && session.UserVerification == protocol.VerificationRequired && !session.Expires.IsZero() && session.Expires.After(time.Now()) && !session.Expires.After(time.Now().Add(passkeyLifetime+time.Second))
}

func (p *passkeyProtocol) registration(user passkeyUser, session webauthn.SessionData, input json.RawMessage) (webauthn.Credential, error) {
	if !p.validSession(user, session) || len(input) == 0 || len(input) > passkeyResponseLimit {
		return webauthn.Credential{}, ErrDenied
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(input)
	if err != nil {
		return webauthn.Credential{}, ErrDenied
	}
	credential, err := p.web.CreateCredential(user, session, parsed)
	if err != nil || credential == nil || !credential.Flags.UserPresent || !credential.Flags.UserVerified || len(credential.ID) == 0 || len(credential.ID) > 1023 || len(credential.PublicKey) == 0 || len(credential.PublicKey) > 4096 {
		return webauthn.Credential{}, ErrDenied
	}
	return *credential, nil
}

func (p *passkeyProtocol) login(user passkeyUser, session webauthn.SessionData, input json.RawMessage) (webauthn.Credential, error) {
	if !p.validSession(user, session) || len(input) == 0 || len(input) > passkeyResponseLimit {
		return webauthn.Credential{}, ErrDenied
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(input)
	if err != nil {
		return webauthn.Credential{}, ErrDenied
	}
	credential, err := p.web.ValidateLogin(user, session, parsed)
	if err != nil || credential == nil || !parsed.Response.AuthenticatorData.Flags.HasUserPresent() || !parsed.Response.AuthenticatorData.Flags.HasUserVerified() || credential.Authenticator.CloneWarning {
		return webauthn.Credential{}, ErrDenied
	}
	return *credential, nil
}
