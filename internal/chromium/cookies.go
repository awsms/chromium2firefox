package chromium

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	chromiumCookiePrefixV10 = "v10"
	chromiumCookiePrefixV11 = "v11"
)

type Cookie struct {
	HostKey              string
	TopFrameSiteKey      string
	Name                 string
	Value                string
	Path                 string
	ExpiresUnixMillis    int64
	LastAccessUnixMicros int64
	CreationUnixMicros   int64
	UpdateUnixMicros     int64
	IsSecure             bool
	IsHTTPOnly           bool
	SameSite             int
	SourceScheme         int
	IsPartitioned        bool
}

func ReadCookies(ctx context.Context, cookiesPath string) ([]Cookie, error) {
	db, err := sql.Open("sqlite", cookiesPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
SELECT
	host_key,
	top_frame_site_key,
	name,
	value,
	encrypted_value,
	path,
	expires_utc,
	last_access_utc,
	creation_utc,
	last_update_utc,
	is_secure,
	is_httponly,
	samesite,
	source_scheme,
	has_expires,
	is_persistent
FROM cookies
ORDER BY last_access_utc ASC
`)
	if err != nil {
		return nil, fmt.Errorf("query cookies: %w", err)
	}
	defer rows.Close()

	v11Password, v11PasswordErr := lookupV11Password(ctx)

	var out []Cookie
	for rows.Next() {
		var (
			item           Cookie
			plaintextValue string
			encryptedValue []byte
			isSecure       int
			isHTTPOnly     int
			hasExpires     int
			isPersistent   int
		)
		if err := rows.Scan(
			&item.HostKey,
			&item.TopFrameSiteKey,
			&item.Name,
			&plaintextValue,
			&encryptedValue,
			&item.Path,
			&item.ExpiresUnixMillis,
			&item.LastAccessUnixMicros,
			&item.CreationUnixMicros,
			&item.UpdateUnixMicros,
			&isSecure,
			&isHTTPOnly,
			&item.SameSite,
			&item.SourceScheme,
			&hasExpires,
			&isPersistent,
		); err != nil {
			return nil, fmt.Errorf("scan cookie row: %w", err)
		}

		if plaintextValue != "" {
			item.Value = plaintextValue
		} else {
			value, err := decryptCookieValue(encryptedValue, v11Password, v11PasswordErr)
			if err != nil {
				return nil, fmt.Errorf("decrypt cookie %s for %s: %w", item.Name, item.HostKey, err)
			}
			item.Value = value
		}

		item.ExpiresUnixMillis = chromiumMicrosToUnixMillis(item.ExpiresUnixMillis)
		item.LastAccessUnixMicros = chromiumMicrosToUnixMicros(item.LastAccessUnixMicros)
		item.CreationUnixMicros = chromiumMicrosToUnixMicros(item.CreationUnixMicros)
		item.UpdateUnixMicros = chromiumMicrosToUnixMicros(item.UpdateUnixMicros)
		item.IsSecure = isSecure != 0
		item.IsHTTPOnly = isHTTPOnly != 0
		item.IsPartitioned = item.TopFrameSiteKey != ""

		if hasExpires == 0 || isPersistent == 0 {
			item.ExpiresUnixMillis = 0
		}

		out = append(out, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cookies: %w", err)
	}

	return out, nil
}

func decryptCookieValue(ciphertext []byte, v11Password string, v11PasswordErr error) (string, error) {
	switch {
	case bytes.HasPrefix(ciphertext, []byte(chromiumCookiePrefixV10)):
		plaintext, err := decryptCBC(ciphertext[len(chromiumCookiePrefixV10):], deriveLinuxCookieKey("peanuts"))
		if err != nil {
			return "", err
		}
		return string(plaintext), nil
	case bytes.HasPrefix(ciphertext, []byte(chromiumCookiePrefixV11)):
		if v11PasswordErr != nil {
			return "", v11PasswordErr
		}
		plaintext, err := decryptCBC(ciphertext[len(chromiumCookiePrefixV11):], deriveLinuxCookieKey(v11Password))
		if err != nil {
			return "", err
		}
		return string(plaintext), nil
	default:
		return string(ciphertext), nil
	}
}

func lookupV11Password(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "secret-tool", "lookup", "application", "chromium")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("lookup chromium safe storage password: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func deriveLinuxCookieKey(password string) []byte {
	return pbkdf2HMACSHA1([]byte(password), []byte("saltysalt"), 1, 16)
}

func decryptCBC(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of %d", len(ciphertext), aes.BlockSize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	iv := bytes.Repeat([]byte(" "), aes.BlockSize)
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	return pkcs7Unpad(plaintext, aes.BlockSize)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("invalid padded data length %d", len(data))
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, fmt.Errorf("invalid padding length %d", pad)
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, fmt.Errorf("invalid PKCS#7 padding")
		}
	}
	return data[:len(data)-pad], nil
}

func pbkdf2HMACSHA1(password, salt []byte, iterations, keyLen int) []byte {
	hLen := 20
	numBlocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, numBlocks*hLen)

	for block := 1; block <= numBlocks; block++ {
		u := hmacSHA1(password, append(salt, byte(block>>24), byte(block>>16), byte(block>>8), byte(block)))
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			u = hmacSHA1(password, u)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}

	return out[:keyLen]
}

func hmacSHA1(key, data []byte) []byte {
	const blockSize = 64
	if len(key) > blockSize {
		sum := sha1.Sum(key)
		key = sum[:]
	}
	k := make([]byte, blockSize)
	copy(k, key)

	oKeyPad := make([]byte, blockSize)
	iKeyPad := make([]byte, blockSize)
	for i := 0; i < blockSize; i++ {
		oKeyPad[i] = k[i] ^ 0x5c
		iKeyPad[i] = k[i] ^ 0x36
	}

	inner := sha1.New()
	inner.Write(iKeyPad)
	inner.Write(data)
	innerSum := inner.Sum(nil)

	outer := sha1.New()
	outer.Write(oKeyPad)
	outer.Write(innerSum)
	return outer.Sum(nil)
}

func chromiumMicrosToUnixMicros(value int64) int64 {
	const chromiumEpochOffsetMicros = int64(11644473600) * 1_000_000
	return value - chromiumEpochOffsetMicros
}

func chromiumMicrosToUnixMillis(value int64) int64 {
	if value == 0 {
		return 0
	}
	return chromiumMicrosToUnixMicros(value) / 1000
}

func FirefoxOriginAttributes(topFrameSiteKey string) (string, error) {
	if topFrameSiteKey == "" {
		return "", nil
	}

	parsed, err := url.Parse(topFrameSiteKey)
	if err != nil {
		return "", fmt.Errorf("parse top frame site key %q: %w", topFrameSiteKey, err)
	}

	host := parsed.Hostname()
	if parsed.Scheme == "" || host == "" {
		return "", fmt.Errorf("unsupported top frame site key %q", topFrameSiteKey)
	}

	partitionKey := fmt.Sprintf("(%s,%s)", parsed.Scheme, host)
	return "^partitionKey=" + url.QueryEscape(partitionKey), nil
}
