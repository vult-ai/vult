package access

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// 64 MiB, three passes, one lane. One verification at a time per Go process;
// account-wide PostgreSQL throttling bounds work across clients and replicas.
const passwordMemory uint32 = 64 * 1024
const passwordTime uint32 = 3

func hashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < 16 || len(password) > 1024 || strings.ContainsRune(password, 0) {
		return "", fmt.Errorf("use a passphrase of at least 16 characters and at most 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	key := argon2.IDKey([]byte(password), salt, passwordTime, passwordMemory, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=1$%s$%s", passwordMemory, passwordTime, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Parse and clamp before allocation. A malformed/restored hash cannot ask the
// verifier for arbitrary RAM, threads or CPU. Older accepted parameters upgrade
// only after both factors succeed.
func verifyPassword(password, phc string) (valid, upgrade bool) {
	if len(password) > 1024 || len(phc) > 512 {
		return false, false
	}
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, false
	}
	values := strings.Split(parts[3], ",")
	if len(values) != 3 {
		return false, false
	}
	params := []uint64{}
	for i, name := range []string{"m=", "t=", "p="} {
		if !strings.HasPrefix(values[i], name) {
			return false, false
		}
		n, e := strconv.ParseUint(strings.TrimPrefix(values[i], name), 10, 32)
		if e != nil {
			return false, false
		}
		params = append(params, n)
	}
	memory, passes, lanes := params[0], params[1], params[2]
	if memory < 19*1024 || memory > uint64(passwordMemory) || passes < 2 || passes > 4 || lanes < 1 || lanes > 2 {
		return false, false
	}
	salt, e := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if e != nil || len(salt) < 16 || len(salt) > 32 {
		return false, false
	}
	expected, e := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if e != nil || len(expected) != 32 {
		return false, false
	}
	actual := argon2.IDKey([]byte(password), salt, uint32(passes), uint32(memory), uint8(lanes), 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1, memory != uint64(passwordMemory) || passes != uint64(passwordTime) || lanes != 1
}
