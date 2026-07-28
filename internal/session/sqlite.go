package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	_ "modernc.org/sqlite"
)

const encryptedArchiveHeader = "CINLAN_SESSION_ARCHIVE_V1\n"

type sqliteBackend struct {
	db     *sql.DB
	cipher *fieldCipher
}

type fieldCipher struct {
	aead   cipher.AEAD
	macKey []byte
}

func OpenEncryptedSQLite(
	maxHistory int,
	ttl time.Duration,
	path, encodedKey, legacyPath string,
) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("SQLite session store path is empty")
	}
	crypt, err := newFieldCipher(encodedKey)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create SQLite session directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite session store: %w", err)
	}
	db.SetMaxOpenConns(1)
	backend := &sqliteBackend{db: db, cipher: crypt}
	store := newWithClock(maxHistory, ttl, time.Now)
	store.path = path
	store.sqlite = backend
	if err := backend.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.loadSQLite(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrateLegacy(strings.TrimSpace(legacyPath)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func newFieldCipher(encodedKey string) (*fieldCipher, error) {
	key, err := decodeEncryptionKey(strings.TrimSpace(encodedKey))
	if err != nil {
		return nil, err
	}
	derived := sha512.Sum512(append([]byte("cinlan-qq-bot/session/v1\x00"), key...))
	block, err := aes.NewCipher(derived[:32])
	if err != nil {
		return nil, fmt.Errorf("initialize session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize session AEAD: %w", err)
	}
	return &fieldCipher{
		aead:   aead,
		macKey: append([]byte(nil), derived[32:]...),
	}, nil
}

func decodeEncryptionKey(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("SESSION_ENCRYPTION_KEY is required for encrypted SQLite sessions")
	}
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, decode := range decoders {
		key, err := decode(value)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, errors.New("SESSION_ENCRYPTION_KEY must encode exactly 32 bytes using base64 or hex")
}

func (c *fieldCipher) scopeHash(scope string) []byte {
	mac := hmac.New(sha512.New512_256, c.macKey)
	_, _ = mac.Write([]byte("scope\x00"))
	_, _ = mac.Write([]byte(scope))
	return mac.Sum(nil)
}

func (c *fieldCipher) seal(label string, scopeHash, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate session nonce: %w", err)
	}
	aad := fieldAAD(label, scopeHash)
	return c.aead.Seal(nonce, nonce, plaintext, aad), nil
}

func (c *fieldCipher) open(label string, scopeHash, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) <= nonceSize {
		return nil, errors.New("encrypted session field is truncated")
	}
	plaintext, err := c.aead.Open(
		nil,
		ciphertext[:nonceSize],
		ciphertext[nonceSize:],
		fieldAAD(label, scopeHash),
	)
	if err != nil {
		return nil, errors.New("decrypt session field: authentication failed")
	}
	return plaintext, nil
}

func fieldAAD(label string, scopeHash []byte) []byte {
	aad := make([]byte, 0, len(label)+1+len(scopeHash))
	aad = append(aad, label...)
	aad = append(aad, 0)
	return append(aad, scopeHash...)
}

func (b *sqliteBackend) initialize() error {
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=FULL`,
		`PRAGMA secure_delete=ON`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS sessions (
			scope_hash BLOB PRIMARY KEY,
			scope_cipher BLOB NOT NULL,
			handoff INTEGER NOT NULL DEFAULT 0,
			persona_cipher BLOB,
			provider_cipher BLOB,
			summary_cipher BLOB,
			memory_cipher BLOB,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			scope_hash BLOB NOT NULL,
			seq INTEGER NOT NULL,
			role TEXT NOT NULL,
			content_cipher BLOB NOT NULL,
			PRIMARY KEY (scope_hash, seq),
			FOREIGN KEY (scope_hash) REFERENCES sessions(scope_hash) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS markers (
			marker_hash BLOB PRIMARY KEY,
			marker_cipher BLOB NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions(updated_at)`,
	} {
		if _, err := b.db.Exec(statement); err != nil {
			return fmt.Errorf("initialize SQLite session store: %w", err)
		}
	}
	return nil
}

func (s *Store) loadSQLite() error {
	rows, err := s.sqlite.db.Query(`
		SELECT scope_hash, scope_cipher, handoff, persona_cipher,
		       provider_cipher, summary_cipher, memory_cipher, updated_at
		FROM sessions`)
	if err != nil {
		return fmt.Errorf("query SQLite sessions: %w", err)
	}
	defer rows.Close()
	hashToScope := make(map[string]string)
	for rows.Next() {
		var (
			scopeHash, scopeCipher        []byte
			personaCipher, providerCipher []byte
			summaryCipher, memoryCipher   []byte
			handoff                       int
			updatedAt                     int64
		)
		if err := rows.Scan(
			&scopeHash,
			&scopeCipher,
			&handoff,
			&personaCipher,
			&providerCipher,
			&summaryCipher,
			&memoryCipher,
			&updatedAt,
		); err != nil {
			return fmt.Errorf("scan SQLite session: %w", err)
		}
		scope, err := s.sqlite.cipher.open("scope", scopeHash, scopeCipher)
		if err != nil {
			return err
		}
		if !hmac.Equal(scopeHash, s.sqlite.cipher.scopeHash(string(scope))) {
			return errors.New("SQLite session scope hash does not match encrypted identity")
		}
		decryptText := func(label string, value []byte) (string, error) {
			plain, decryptErr := s.sqlite.cipher.open(label, scopeHash, value)
			return string(plain), decryptErr
		}
		persona, err := decryptText("persona", personaCipher)
		if err != nil {
			return err
		}
		provider, err := decryptText("provider", providerCipher)
		if err != nil {
			return err
		}
		summary, err := decryptText("summary", summaryCipher)
		if err != nil {
			return err
		}
		memory, err := decryptText("memory", memoryCipher)
		if err != nil {
			return err
		}
		scopeID := string(scope)
		s.entries[scopeID] = entry{
			Handoff:   handoff != 0,
			Persona:   persona,
			Provider:  provider,
			Summary:   summary,
			Memory:    memory,
			UpdatedAt: time.Unix(0, updatedAt),
		}
		hashToScope[hex.EncodeToString(scopeHash)] = scopeID
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate SQLite sessions: %w", err)
	}

	messageRows, err := s.sqlite.db.Query(`
		SELECT scope_hash, seq, role, content_cipher
		FROM messages
		ORDER BY scope_hash, seq`)
	if err != nil {
		return fmt.Errorf("query SQLite session messages: %w", err)
	}
	defer messageRows.Close()
	for messageRows.Next() {
		var scopeHash, contentCipher []byte
		var seq int
		var role string
		if err := messageRows.Scan(&scopeHash, &seq, &role, &contentCipher); err != nil {
			return fmt.Errorf("scan SQLite session message: %w", err)
		}
		scopeID, ok := hashToScope[hex.EncodeToString(scopeHash)]
		if !ok {
			return errors.New("SQLite session message references an unknown scope")
		}
		label := fmt.Sprintf("message:%d:%s", seq, role)
		content, err := s.sqlite.cipher.open(label, scopeHash, contentCipher)
		if err != nil {
			return err
		}
		current := s.entries[scopeID]
		current.History = append(current.History, domain.ChatMessage{
			Role:    role,
			Content: string(content),
		})
		s.entries[scopeID] = current
	}
	if err := messageRows.Err(); err != nil {
		return fmt.Errorf("iterate SQLite session messages: %w", err)
	}
	markerRows, err := s.sqlite.db.Query(`
		SELECT marker_hash, marker_cipher, created_at
		FROM markers`)
	if err != nil {
		return fmt.Errorf("query SQLite markers: %w", err)
	}
	defer markerRows.Close()
	for markerRows.Next() {
		var markerHash, markerCipher []byte
		var createdAt int64
		if err := markerRows.Scan(&markerHash, &markerCipher, &createdAt); err != nil {
			return fmt.Errorf("scan SQLite marker: %w", err)
		}
		key, err := s.sqlite.cipher.open("marker", markerHash, markerCipher)
		if err != nil {
			return err
		}
		expectedHash := s.sqlite.cipher.scopeHash("marker\x00" + string(key))
		if !hmac.Equal(markerHash, expectedHash) {
			return errors.New("SQLite marker hash does not match encrypted key")
		}
		s.markers[string(key)] = time.Unix(0, createdAt)
	}
	if err := markerRows.Err(); err != nil {
		return fmt.Errorf("iterate SQLite markers: %w", err)
	}
	return nil
}

func (s *Store) claimSQLiteMarkerLocked(
	key string,
	createdAt time.Time,
) (bool, error) {
	markerHash := s.sqlite.cipher.scopeHash("marker\x00" + key)
	markerCipher, err := s.sqlite.cipher.seal(
		"marker",
		markerHash,
		[]byte(key),
	)
	if err != nil {
		return false, err
	}
	result, err := s.sqlite.db.Exec(`
		INSERT INTO markers (marker_hash, marker_cipher, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(marker_hash) DO NOTHING`,
		markerHash,
		markerCipher,
		createdAt.UnixNano(),
	)
	if err != nil {
		return false, fmt.Errorf("insert SQLite marker: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read SQLite marker insert result: %w", err)
	}
	return affected == 1, nil
}

func (s *Store) releaseSQLiteMarkerLocked(key string) error {
	markerHash := s.sqlite.cipher.scopeHash("marker\x00" + key)
	if _, err := s.sqlite.db.Exec(
		`DELETE FROM markers WHERE marker_hash = ?`,
		markerHash,
	); err != nil {
		return fmt.Errorf("delete SQLite marker: %w", err)
	}
	return nil
}

func (s *Store) persistSQLiteLocked() error {
	tx, err := s.sqlite.db.Begin()
	if err != nil {
		return fmt.Errorf("begin SQLite session transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM messages`); err != nil {
		return fmt.Errorf("clear SQLite session messages: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return fmt.Errorf("clear SQLite sessions: %w", err)
	}
	for scope, current := range s.entries {
		scopeHash := s.sqlite.cipher.scopeHash(scope)
		sealText := func(label, value string) ([]byte, error) {
			return s.sqlite.cipher.seal(label, scopeHash, []byte(value))
		}
		scopeCipher, err := s.sqlite.cipher.seal("scope", scopeHash, []byte(scope))
		if err != nil {
			return err
		}
		personaCipher, err := sealText("persona", current.Persona)
		if err != nil {
			return err
		}
		providerCipher, err := sealText("provider", current.Provider)
		if err != nil {
			return err
		}
		summaryCipher, err := sealText("summary", current.Summary)
		if err != nil {
			return err
		}
		memoryCipher, err := sealText("memory", current.Memory)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO sessions (
				scope_hash, scope_cipher, handoff, persona_cipher,
				provider_cipher, summary_cipher, memory_cipher, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			scopeHash,
			scopeCipher,
			boolInt(current.Handoff),
			personaCipher,
			providerCipher,
			summaryCipher,
			memoryCipher,
			current.UpdatedAt.UnixNano(),
		); err != nil {
			return fmt.Errorf("insert SQLite session: %w", err)
		}
		for seq, message := range current.History {
			label := fmt.Sprintf("message:%d:%s", seq, message.Role)
			contentCipher, err := s.sqlite.cipher.seal(
				label,
				scopeHash,
				[]byte(message.Content),
			)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`
				INSERT INTO messages (scope_hash, seq, role, content_cipher)
				VALUES (?, ?, ?, ?)`,
				scopeHash,
				seq,
				message.Role,
				contentCipher,
			); err != nil {
				return fmt.Errorf("insert SQLite session message: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite session transaction: %w", err)
	}
	return nil
}

func (s *Store) migrateLegacy(path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy session store: %w", err)
	}
	if len(data) > maxPersistedBytes {
		return fmt.Errorf("legacy session store exceeds %d bytes", maxPersistedBytes)
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode legacy session store: %w", err)
	}
	if state.Version != 0 && state.Version != 1 {
		return fmt.Errorf("unsupported legacy session store version %d", state.Version)
	}
	changed := false
	for key, current := range state.Sessions {
		if strings.TrimSpace(key) == "" || current.UpdatedAt.IsZero() {
			continue
		}
		existing, exists := s.entries[key]
		if exists && !current.UpdatedAt.After(existing.UpdatedAt) {
			continue
		}
		current.History = cloneMessages(current.History)
		s.entries[key] = current
		changed = true
	}
	if changed {
		if err := s.persistSQLiteLocked(); err != nil {
			return fmt.Errorf("persist migrated sessions: %w", err)
		}
	}
	archivePath := path + ".migrated.enc"
	archiveHash := s.sqlite.cipher.scopeHash(filepath.Clean(path))
	ciphertext, err := s.sqlite.cipher.seal("legacy-archive", archiveHash, data)
	if err != nil {
		return err
	}
	encoded := append(
		[]byte(encryptedArchiveHeader),
		[]byte(base64.RawStdEncoding.EncodeToString(ciphertext))...,
	)
	if err := writePrivateAtomic(archivePath, encoded); err != nil {
		return fmt.Errorf("write encrypted legacy session archive: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove plaintext legacy session store: %w", err)
	}
	return nil
}

func writePrivateAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	temp, err := os.CreateTemp(dir, ".session-archive-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err = temp.Write(data); err == nil {
		err = temp.Close()
	} else {
		_ = temp.Close()
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(path)
		if err := os.Rename(tempName, path); err != nil {
			return err
		}
	}
	return os.Chmod(path, 0o600)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
