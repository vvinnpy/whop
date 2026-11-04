// Package secrets provides a local, password-protected secrets vault.
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gofrs/flock"
	"github.com/natefinch/atomic"
	"golang.org/x/crypto/argon2"
)

const (
	vaultVersion = uint16(1)
	keySize      = 32
	saltSize     = 16
	nonceSize    = 12

	// File layout:
	//   magic[8] | version[2] | time[4] | memory[4] | threads[1] |
	//   reserved[1] | saltLen[1] | nonceLen[1] | salt[16] | nonce[12] |
	//   ciphertext[GCM-tagged payload]
	headerSize = 8 + 2 + 4 + 4 + 1 + 1 + 1 + 1 + saltSize + nonceSize

	// Argon2 memory is expressed in KiB.
	maxMemoryKiB = 256 * 1024
	maxTime      = 64
	maxThreads   = 32
)

var (
	// ErrInvalidPassword means the master password did not decrypt the vault.
	//
	// AES-GCM cannot distinguish an incorrect password from an authenticated
	// ciphertext failure, so this error also covers corrupted ciphertext.
	ErrInvalidPassword = errors.New("secrets: invalid master password")

	// ErrLocked means a Vault operation was attempted after Close.
	ErrLocked = errors.New("secrets: vault is closed")

	// ErrInvalidVault means the file header or decrypted payload is malformed.
	ErrInvalidVault = errors.New("secrets: invalid vault")
)

var vaultMagic = [8]byte{'T', 'T', 'Y', 'D', 'V', 'L', 'T', 0}

var defaultParams = Params{
	Time:    3,
	Memory:  64 * 1024,
	Threads: 2,
}

// Params controls the Argon2id key derivation function.
//
// Memory is measured in KiB. Defaults are time=3, memory=64 MiB, threads=2.
type Params struct {
	Time    uint32
	Memory  uint32
	Threads uint8
}

// Vault is an in-memory view of an encrypted secrets file.
//
// A Vault is safe for concurrent use from multiple goroutines. Changes made
// with Set or Delete are only persisted by an explicit Save.
type Vault struct {
	mu      sync.RWMutex
	path    string
	params  Params
	salt    [saltSize]byte
	key     [keySize]byte
	secrets map[string]string
	closed  bool
}

// DefaultPath returns the per-user vault path.
//
// The path is under the platform user configuration directory and the parent
// directory may not exist until Create is called.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("secrets: default path: %w", err)
	}
	return filepath.Join(dir, "whop", "secrets.vault"), nil
}

// Create creates a new empty vault protected by masterPassword.
//
// The password and derived key are never written to disk. The file is created
// with an atomic replace and mode 0600. Create refuses to overwrite an existing
// file.
func Create(path, masterPassword string) (*Vault, error) {
	return createWithParams(path, masterPassword, defaultParams)
}

// CreateWithParams is equivalent to Create but accepts custom KDF parameters.
//
// It is intended for tests and advanced embedders. Production callers should
// use Create because it applies the secure defaults.
func CreateWithParams(path, masterPassword string, params Params) (*Vault, error) {
	return createWithParams(path, masterPassword, params)
}

func createWithParams(path, masterPassword string, params Params) (*Vault, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("secrets: empty vault path")
	}
	if masterPassword == "" {
		return nil, errors.New("secrets: empty master password")
	}
	if err := validateParams(params); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secrets: create vault directory: %w", err)
	}

	var salt [saltSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, fmt.Errorf("secrets: generate salt: %w", err)
	}
	key := deriveKey(masterPassword, salt[:], params)
	defer zeroKey(&key)
	v := &Vault{
		path:    path,
		params:  params,
		salt:    salt,
		key:     key,
		secrets: make(map[string]string),
	}
	created := false
	defer func() {
		if !created {
			// Clearing on failure keeps the key lifetime short.
			_ = v.Close()
		}
	}()

	err := withFileLock(path, false, func() error {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("secrets: vault already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secrets: stat vault: %w", err)
		}
		data, err := sealVault(params, salt, key, v.secrets)
		if err != nil {
			return err
		}
		defer zeroBytes(data)
		return writeVaultFile(path, data)
	})
	if err != nil {
		return nil, err
	}
	created = true
	return v, nil
}

// Open opens and decrypts an existing vault.
//
// The file is read under a shared advisory lock. A successful call returns a
// Vault with its secrets in memory; call Close when finished.
func Open(path, masterPassword string) (*Vault, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("secrets: empty vault path")
	}
	if masterPassword == "" {
		return nil, errors.New("secrets: empty master password")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("secrets: stat vault: %w", err)
	}

	var data []byte
	if err := withFileLock(path, true, func() error {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("secrets: read vault: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	v, err := openBytes(path, masterPassword, data)
	zeroBytes(data)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func openBytes(path, masterPassword string, data []byte) (*Vault, error) {
	header, params, salt, nonce, ciphertext, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	key := deriveKey(masterPassword, salt[:], params)
	defer zeroKey(&key)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("secrets: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: create GCM: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce[:], ciphertext, header)
	if err != nil {
		return nil, fmt.Errorf("%w: decryption failed", ErrInvalidPassword)
	}
	defer zeroBytes(plaintext)

	var payload vaultPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("%w: malformed payload: %v", ErrInvalidVault, err)
	}
	if payload.Version != 1 {
		return nil, fmt.Errorf("%w: unsupported payload version %d", ErrInvalidVault, payload.Version)
	}
	if payload.Secrets == nil {
		payload.Secrets = make(map[string]string)
	}
	return &Vault{
		path:    path,
		params:  params,
		salt:    salt,
		key:     key,
		secrets: payload.Secrets,
	}, nil
}

// Get returns the secret for name.
//
// The returned error is ErrLocked after Close.
func (v *Vault) Get(name string) (string, bool, error) {
	if v == nil {
		return "", false, ErrLocked
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.closed {
		return "", false, ErrLocked
	}
	if err := validateName(name); err != nil {
		return "", false, err
	}
	secret, ok := v.secrets[name]
	return secret, ok, nil
}

// Set stores secret in memory.
//
// Changes are not persisted until Save is called. Empty names and empty
// secrets are rejected. Secret values may contain arbitrary Unicode text.
func (v *Vault) Set(name, secret string) error {
	if v == nil {
		return ErrLocked
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ErrLocked
	}
	if err := validateName(name); err != nil {
		return err
	}
	if secret == "" {
		return errors.New("secrets: empty secret value")
	}
	v.secrets[name] = secret
	return nil
}

// Delete removes name from memory.
//
// Changes are not persisted until Save is called. Deleting a missing name is
// not an error. The returned error is ErrLocked after Close.
func (v *Vault) Delete(name string) error {
	if v == nil {
		return ErrLocked
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ErrLocked
	}
	if err := validateName(name); err != nil {
		return err
	}
	delete(v.secrets, name)
	return nil
}

// List returns the stored names in sorted order.
//
// The returned error is ErrLocked after Close.
func (v *Vault) List() ([]string, error) {
	if v == nil {
		return nil, ErrLocked
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.closed {
		return nil, ErrLocked
	}
	names := make([]string, 0, len(v.secrets))
	for name := range v.secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Save encrypts the current in-memory map and atomically replaces the vault
// file. Save must be called explicitly after Set and Delete. It uses a fresh
// random nonce on every write and a cross-process advisory lock.
func (v *Vault) Save() error {
	if v == nil {
		return ErrLocked
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ErrLocked
	}
	data, err := sealVault(v.params, v.salt, v.key, v.secrets)
	if err != nil {
		return err
	}
	defer zeroBytes(data)
	return withFileLock(v.path, false, func() error {
		return writeVaultFile(v.path, data)
	})
}

// Close releases the in-memory key and secrets.
//
// It does not implicitly save changes. Calling Close more than once is safe.
func (v *Vault) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true
	zeroKey(&v.key)
	for name := range v.secrets {
		delete(v.secrets, name)
	}
	v.secrets = nil
	return nil
}

type vaultPayload struct {
	Version int               `json:"version"`
	Secrets map[string]string `json:"secrets"`
}

func validateParams(params Params) error {
	if params.Time == 0 || params.Time > maxTime {
		return fmt.Errorf("secrets: invalid KDF time %d", params.Time)
	}
	if params.Threads == 0 || params.Threads > maxThreads {
		return fmt.Errorf("secrets: invalid KDF threads %d", params.Threads)
	}
	if params.Memory < uint32(params.Threads)*8 {
		return fmt.Errorf("secrets: KDF memory %d KiB is too small", params.Memory)
	}
	if params.Memory > maxMemoryKiB {
		return fmt.Errorf("secrets: KDF memory %d KiB exceeds limit", params.Memory)
	}
	return nil
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("secrets: empty secret name")
	}
	return nil
}

func deriveKey(masterPassword string, salt []byte, params Params) [keySize]byte {
	password := []byte(masterPassword)
	defer zeroBytes(password)
	derived := argon2.IDKey(
		password,
		salt,
		params.Time,
		params.Memory,
		params.Threads,
		keySize,
	)
	var key [keySize]byte
	copy(key[:], derived)
	zeroBytes(derived)
	return key
}

func sealVault(params Params, salt [saltSize]byte, key [keySize]byte, secrets map[string]string) ([]byte, error) {
	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("secrets: generate nonce: %w", err)
	}
	header := make([]byte, headerSize)
	copy(header[:8], vaultMagic[:])
	binary.BigEndian.PutUint16(header[8:10], vaultVersion)
	binary.BigEndian.PutUint32(header[10:14], params.Time)
	binary.BigEndian.PutUint32(header[14:18], params.Memory)
	header[18] = params.Threads
	header[19] = 0 // reserved
	header[20] = saltSize
	header[21] = nonceSize
	copy(header[22:22+saltSize], salt[:])
	copy(header[22+saltSize:22+saltSize+nonceSize], nonce[:])

	plaintext, err := json.Marshal(vaultPayload{
		Version: 1,
		Secrets: secrets,
	})
	if err != nil {
		return nil, fmt.Errorf("secrets: encode payload: %w", err)
	}
	defer zeroBytes(plaintext)

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("secrets: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: create GCM: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce[:], plaintext, header)
	result := make([]byte, 0, len(header)+len(ciphertext))
	result = append(result, header...)
	result = append(result, ciphertext...)
	return result, nil
}

func parseHeader(data []byte) ([]byte, Params, [saltSize]byte, [nonceSize]byte, []byte, error) {
	var salt [saltSize]byte
	var nonce [nonceSize]byte
	if len(data) < headerSize+16 {
		return nil, Params{}, salt, nonce, nil, fmt.Errorf("%w: file too short", ErrInvalidVault)
	}
	if !bytes.Equal(data[:8], vaultMagic[:]) {
		return nil, Params{}, salt, nonce, nil, fmt.Errorf("%w: invalid magic", ErrInvalidVault)
	}
	if version := binary.BigEndian.Uint16(data[8:10]); version != vaultVersion {
		return nil, Params{}, salt, nonce, nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidVault, version)
	}
	if data[19] != 0 {
		return nil, Params{}, salt, nonce, nil, fmt.Errorf("%w: invalid reserved header byte", ErrInvalidVault)
	}
	if data[20] != saltSize || data[21] != nonceSize {
		return nil, Params{}, salt, nonce, nil, fmt.Errorf("%w: invalid salt or nonce length", ErrInvalidVault)
	}
	params := Params{
		Time:    binary.BigEndian.Uint32(data[10:14]),
		Memory:  binary.BigEndian.Uint32(data[14:18]),
		Threads: data[18],
	}
	if err := validateParams(params); err != nil {
		return nil, Params{}, salt, nonce, nil, fmt.Errorf("%w: %v", ErrInvalidVault, err)
	}
	copy(salt[:], data[22:22+saltSize])
	copy(nonce[:], data[22+saltSize:22+saltSize+nonceSize])
	header := append([]byte(nil), data[:headerSize]...)
	ciphertext := append([]byte(nil), data[headerSize:]...)
	return header, params, salt, nonce, ciphertext, nil
}

func writeVaultFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".whop-vault-*")
	if err != nil {
		return fmt.Errorf("secrets: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	defer cleanup()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secrets: chmod temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secrets: write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secrets: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("secrets: close temporary file: %w", err)
	}
	if err := atomic.ReplaceFile(tmpName, path); err != nil {
		return fmt.Errorf("secrets: replace vault: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secrets: chmod vault: %w", err)
	}
	return nil
}

func withFileLock(path string, shared bool, fn func() error) (retErr error) {
	lock := flock.New(path+".lock", flock.SetPermissions(0o600))
	if shared {
		if err := lock.RLock(); err != nil {
			return fmt.Errorf("secrets: lock vault: %w", err)
		}
	} else {
		if err := lock.Lock(); err != nil {
			return fmt.Errorf("secrets: lock vault: %w", err)
		}
	}
	defer func() {
		if err := lock.Unlock(); retErr == nil && err != nil {
			retErr = fmt.Errorf("secrets: unlock vault: %w", err)
		}
	}()
	return fn()
}

func zeroKey(key *[keySize]byte) {
	for i := range key {
		key[i] = 0
	}
}

func zeroBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
