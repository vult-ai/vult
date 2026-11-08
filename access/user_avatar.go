package access

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/jpeg"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

const avatarMediaType = "image/jpeg"
const maxAvatarURL = 196608
const maxAvatarBytes = 147456
const avatarSide = 256

type AvatarAsset struct {
	ID          string `json:"id"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
}

type UserProfilePatch struct {
	DisplayName *string         `json:"display_name"`
	Photo       json.RawMessage `json:"photo"`
}

type avatarPatch struct {
	set   bool
	data  []byte
	asset *AvatarAsset
}

func (p avatarPatch) matches(current *AvatarAsset) bool {
	if p.asset == nil || current == nil {
		return p.asset == current
	}
	return p.asset.SHA256 == current.SHA256
}

func parseAvatarPatch(profile *UserProfilePatch) (avatarPatch, error) {
	if profile == nil || profile.Photo == nil {
		return avatarPatch{}, nil
	}
	if bytes.Equal(profile.Photo, []byte("null")) {
		return avatarPatch{set: true}, nil
	}
	var url string
	if err := json.Unmarshal(profile.Photo, &url); err != nil {
		return avatarPatch{}, ledger.ErrInvalid
	}
	if len(url) > maxAvatarURL {
		return avatarPatch{}, ledger.ErrLimit
	}
	encoded, ok := strings.CutPrefix(url, "data:image/jpeg;base64,")
	if !ok || strings.ContainsAny(encoded, "\r\n") {
		return avatarPatch{}, ledger.ErrInvalid
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return avatarPatch{}, ledger.ErrInvalid
	}
	if len(data) > maxAvatarBytes {
		return avatarPatch{}, ledger.ErrLimit
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != avatarSide || cfg.Height != avatarSide ||
		!bytes.HasSuffix(data, []byte{0xff, 0xd9}) {
		return avatarPatch{}, ledger.ErrInvalid
	}
	if _, err = jpeg.Decode(bytes.NewReader(data)); err != nil {
		return avatarPatch{}, ledger.ErrInvalid
	}
	digest := sha256.Sum256(data)
	return avatarPatch{set: true, data: data, asset: &AvatarAsset{
		ID: ledger.ID(), SHA256: hex.EncodeToString(digest[:]), ContentType: avatarMediaType, Size: len(data),
	}}, nil
}

func (s *Service) UserAvatar(ctx context.Context, raw, id, revision string) ([]byte, error) {
	var data []byte
	err := s.withAccountOwner(ctx, raw, func(tx pgx.Tx, owner accountOwner) error {
		err := tx.QueryRow(ctx, `SELECT avatar_bytes FROM access.user_settings
			WHERE identity_id=$1 AND avatar_id=$2 AND avatar_sha256=$3`, owner.id, id, revision).Scan(&data)
		if errors.Is(err, pgx.ErrNoRows) {
			return ledger.ErrNotFound
		}
		return err
	})
	return data, err
}
