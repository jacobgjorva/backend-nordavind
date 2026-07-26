package connector

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// LoadKey henter en 32-byte nøkkel for kryptering av kundens
// databasekredensialer. Foretrekker SECRET_KEY (hex) fra miljøet; faller
// tilbake til en fil ved siden av SQLite-filen for lokal utvikling.
func LoadKey(dbPath string) ([]byte, error) {
	if env := strings.TrimSpace(os.Getenv("SECRET_KEY")); env != "" {
		key, err := hex.DecodeString(env)
		if err != nil || len(key) != 32 {
			return nil, errors.New("SECRET_KEY må være 64 hex-tegn (32 byte)")
		}
		return key, nil
	}
	// Postgres-drift har ingen db-katalog å legge nøkkelfila i — da MÅ nøkkelen
	// komme via SECRET_KEY (innholdet i secret.key). Å dikte en filbane ville
	// generert en NY nøkkel og gjort alle lagrede credentials udekrypterbare.
	if strings.Contains(dbPath, "://") {
		return nil, errors.New("database-URL uten SECRET_KEY i miljøet — sett SECRET_KEY til innholdet i secret.key")
	}
	path := filepath.Join(filepath.Dir(dbPath), "secret.key")
	if data, err := os.ReadFile(path); err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err == nil && len(key) == 32 {
			return key, nil
		}
		return nil, errors.New("ugyldig secret.key")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// Encrypt krypterer med AES-256-GCM; nonce prepends chifferteksten.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func Decrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("ugyldig kryptert data")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}
