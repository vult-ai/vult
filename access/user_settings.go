package access

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

type UserPreferences struct {
	Version        int    `json:"version"`
	Language       string `json:"language"`
	RegionalFormat string `json:"regional_format"`
	DateFormat     string `json:"date_format"`
	TimeFormat     string `json:"time_format"`
	DigitGrouping  bool   `json:"digit_grouping"`
	NegativeStyle  string `json:"negative_style"`
}

type UserProfile struct {
	Version     int          `json:"version"`
	DisplayName string       `json:"display_name"`
	Photo       *string      `json:"photo"`
	PhotoAsset  *AvatarAsset `json:"photo_asset,omitempty"`
}

type UserSettings struct {
	SchemaVersion int             `json:"schema_version"`
	UserID        string          `json:"user_id"`
	Revision      string          `json:"revision"`
	Profile       UserProfile     `json:"profile"`
	Preferences   UserPreferences `json:"preferences"`
	Appearance    string          `json:"appearance"`
}

type UserSettingsPatch struct {
	ExpectedRevision string            `json:"expected_revision"`
	Profile          *UserProfilePatch `json:"profile"`
	Preferences      *PreferencesPatch `json:"preferences"`
	Appearance       *string           `json:"appearance"`
}

type PreferencesPatch struct {
	Language       *string `json:"language"`
	RegionalFormat *string `json:"regional_format"`
	DateFormat     *string `json:"date_format"`
	TimeFormat     *string `json:"time_format"`
	DigitGrouping  *bool   `json:"digit_grouping"`
	NegativeStyle  *string `json:"negative_style"`
}

var regionalFormats = []string{
	"en-AU", "de-AT", "nl-BE", "fr-BE", "pt-BR", "en-CA", "fr-CA", "cs-CZ",
	"da-DK", "fi-FI", "fr-FR", "de-DE", "en-IN", "en-IE", "it-IT", "ja-JP",
	"en-KE", "es-MX", "nl-NL", "en-NZ", "en-NG", "nb-NO", "pl-PL", "pt-PT",
	"en-SG", "en-ZA", "es-ES", "sv-SE", "de-CH", "en-AE", "en-GB", "en-US",
}

func defaultUserSettings(user string) UserSettings {
	return UserSettings{
		SchemaVersion: 1, UserID: user, Revision: "0",
		Profile: UserProfile{Version: 1}, Appearance: "system",
		Preferences: UserPreferences{
			Version: 1, Language: "en", RegionalFormat: "en-US",
			DateFormat: "standard", TimeFormat: "24h", DigitGrouping: true, NegativeStyle: "minus",
		},
	}
}

func replaceSetting[T any](value *T, patch *T) {
	if patch != nil {
		*value = *patch
	}
}

func (q UserSettingsPatch) apply(settings *UserSettings) error {
	replaceSetting(&settings.Appearance, q.Appearance)
	if q.Profile != nil {
		replaceSetting(&settings.Profile.DisplayName, q.Profile.DisplayName)
	}
	p := &settings.Preferences
	if changes := q.Preferences; changes != nil {
		replaceSetting(&p.Language, changes.Language)
		replaceSetting(&p.RegionalFormat, changes.RegionalFormat)
		replaceSetting(&p.DateFormat, changes.DateFormat)
		replaceSetting(&p.TimeFormat, changes.TimeFormat)
		replaceSetting(&p.DigitGrouping, changes.DigitGrouping)
		replaceSetting(&p.NegativeStyle, changes.NegativeStyle)
	}
	name := settings.Profile.DisplayName
	if len(utf16.Encode([]rune(name))) > 80 || strings.ContainsFunc(name, unicode.IsControl) ||
		!slices.Contains([]string{"system", "light", "dark"}, settings.Appearance) ||
		p.Language != "en" || !slices.Contains(regionalFormats, p.RegionalFormat) ||
		!slices.Contains([]string{"standard", "month-day-year", "day-month-year", "iso"}, p.DateFormat) ||
		!slices.Contains([]string{"12h", "24h"}, p.TimeFormat) ||
		!slices.Contains([]string{"minus", "parentheses"}, p.NegativeStyle) {
		return ledger.ErrInvalid
	}
	return nil
}

type accountOwner struct {
	id, userID string
}

func (s *Service) withAccountOwner(ctx context.Context, raw string, call func(pgx.Tx, accountOwner) error) error {
	if !validToken(raw) {
		return ErrDenied
	}
	tx, err := s.owners.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin',$2,true)`, Hash(raw), s.origin); err != nil {
		return err
	}
	const ownerQuery = "SELECT id::text,issuer,subject FROM access.require_account_owner()"
	var id string
	var identity Identity
	if err = tx.QueryRow(ctx, ownerQuery).Scan(&id, &identity.Issuer, &identity.Subject); err != nil {
		return ErrDenied
	}
	if err = call(tx, accountOwner{id, UserID(identity)}); err != nil {
		return err
	}
	var live string
	if err = tx.QueryRow(ctx, "SELECT id::text FROM access.require_account_owner()").Scan(&live); err != nil || live != id {
		return ErrDenied
	}
	return tx.Commit(ctx)
}

func readUserSettings(ctx context.Context, tx pgx.Tx, owner accountOwner, lock bool) (UserSettings, error) {
	out := defaultUserSettings(owner.userID)
	query := `SELECT revision::text,display_name,preferences,appearance,avatar_id::text,avatar_sha256,octet_length(avatar_bytes)
		FROM access.user_settings WHERE identity_id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	var id, hash *string
	var size *int
	err := tx.QueryRow(ctx, query, owner.id).Scan(&out.Revision, &out.Profile.DisplayName, &out.Preferences, &out.Appearance, &id, &hash, &size)
	if errors.Is(err, pgx.ErrNoRows) && !lock {
		return out, nil
	}
	if err != nil {
		return UserSettings{}, err
	}
	if id != nil {
		out.Profile.PhotoAsset = &AvatarAsset{ID: *id, SHA256: *hash, ContentType: avatarMediaType, Size: *size}
	}
	return out, nil
}

func (s *Service) UserSettings(ctx context.Context, raw string) (UserSettings, error) {
	var out UserSettings
	err := s.withAccountOwner(ctx, raw, func(tx pgx.Tx, owner accountOwner) error {
		var err error
		out, err = readUserSettings(ctx, tx, owner, false)
		return err
	})
	return out, err
}

func (s *Service) SaveUserSettings(ctx context.Context, raw string, patch UserSettingsPatch) (UserSettings, error) {
	revision, err := strconv.ParseInt(patch.ExpectedRevision, 10, 64)
	if err != nil || revision < 0 || strconv.FormatInt(revision, 10) != patch.ExpectedRevision ||
		(patch.Profile == nil && patch.Preferences == nil && patch.Appearance == nil) {
		return UserSettings{}, ledger.ErrInvalid
	}
	defaults := defaultUserSettings("")
	probe := defaults
	if err := patch.apply(&probe); err != nil {
		return UserSettings{}, err
	}
	change, err := parseAvatarPatch(patch.Profile)
	if err != nil {
		return UserSettings{}, err
	}
	var out UserSettings
	err = s.withAccountOwner(ctx, raw, func(tx pgx.Tx, owner accountOwner) error {
		if _, err := tx.Exec(ctx, `INSERT INTO access.user_settings(identity_id,revision,display_name,preferences,appearance)
			VALUES($1,0,$2,$3,$4) ON CONFLICT(identity_id) DO NOTHING`,
			owner.id, defaults.Profile.DisplayName, defaults.Preferences, defaults.Appearance); err != nil {
			return err
		}
		before, err := readUserSettings(ctx, tx, owner, true)
		if err != nil {
			return err
		}
		if before.Revision != patch.ExpectedRevision || revision == math.MaxInt64 {
			return ledger.ErrConflict
		}
		out = before
		if err := patch.apply(&out); err != nil {
			return err
		}
		changeAvatar := change.set && !change.matches(before.Profile.PhotoAsset)
		if out == before && !changeAvatar {
			return nil
		}
		if changeAvatar {
			out.Profile.PhotoAsset = change.asset
			var id, hash *string
			if change.asset != nil {
				id, hash = &change.asset.ID, &change.asset.SHA256
			}
			if _, err := tx.Exec(ctx, `UPDATE access.user_settings SET avatar_id=$2,avatar_sha256=$3,avatar_bytes=$4
				WHERE identity_id=$1`, owner.id, id, hash, change.data); err != nil {
				return err
			}
		}
		out.Revision = strconv.FormatInt(revision+1, 10)
		_, err = tx.Exec(ctx, `UPDATE access.user_settings SET revision=$2,display_name=$3,preferences=$4,appearance=$5
			WHERE identity_id=$1`, owner.id, revision+1, out.Profile.DisplayName, out.Preferences, out.Appearance)
		return err
	})
	return out, err
}
