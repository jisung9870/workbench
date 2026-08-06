package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"filippo.io/age"
	"github.com/jisung9870/workbench/internal/config"
	"github.com/jisung9870/workbench/internal/storage"
)

const maxPlaintextSize = 16 << 20

type Vault map[string]map[string]string

type Entry struct {
	Service string `json:"service"`
	Field   string `json:"field"`
}

type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

type NotFoundError struct{ Message string }

func (e *NotFoundError) Error() string { return e.Message }

type Store struct {
	paths            config.Paths
	mu               *sync.Mutex
	verifyCiphertext func([]byte, *age.X25519Identity) error
}

var storeLocks sync.Map

func NewStore(paths config.Paths) *Store {
	lock, _ := storeLocks.LoadOrStore(paths.SecretsFile, &sync.Mutex{})
	return &Store{paths: paths, mu: lock.(*sync.Mutex), verifyCiphertext: verifyCiphertext}
}

func (s *Store) Init() (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireSecretsFileLock(s.paths.SecretsFile + ".lock")
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	identityExists, err := pathExists(s.paths.AgeIdentityFile)
	if err != nil {
		return fmt.Errorf("inspect Workbench age identity destination: %w", err)
	}
	storeExists, err := pathExists(s.paths.SecretsFile)
	if err != nil {
		return fmt.Errorf("inspect Workbench secrets destination: %w", err)
	}
	if identityExists {
		return &ConflictError{Message: "Workbench age identity already exists"}
	}
	if storeExists {
		return &ConflictError{Message: "Workbench secrets store already exists"}
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return fmt.Errorf("generate age identity: %w", err)
	}
	identityBytes := []byte(identity.String() + "\n")
	ciphertext, err := encrypt(identity.Recipient(), Vault{})
	if err != nil {
		return err
	}
	if err := installPair(s.paths.AgeIdentityFile, identityBytes, s.paths.SecretsFile, ciphertext); err != nil {
		return fmt.Errorf("initialize secrets store: %w", err)
	}
	return nil
}

func (s *Store) List(service string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vault, _, err := s.load()
	if err != nil {
		return nil, err
	}
	entries := []Entry{}
	if service != "" {
		fields, ok := vault[service]
		if !ok {
			return nil, &NotFoundError{Message: fmt.Sprintf("secret service %q was not found", service)}
		}
		for field := range fields {
			entries = append(entries, Entry{Service: service, Field: field})
		}
	} else {
		for service, fields := range vault {
			if len(fields) == 0 {
				entries = append(entries, Entry{Service: service})
				continue
			}
			for field := range fields {
				entries = append(entries, Entry{Service: service, Field: field})
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Service == entries[j].Service {
			return entries[i].Field < entries[j].Field
		}
		return entries[i].Service < entries[j].Service
	})
	return entries, nil
}

func (s *Store) Set(service, field string, value []byte, replace bool) (backup string, err error) {
	if !ValidName(service) || !ValidName(field) {
		return "", &InvalidError{Message: "secret service and field names allow only letters, digits, dot, underscore, and hyphen"}
	}
	if len(value) == 0 {
		return "", &InvalidError{Message: "secret value must not be empty"}
	}
	if len(value) > maxPlaintextSize {
		return "", &InvalidError{Message: "secret value is too large"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireSecretsFileLock(s.paths.SecretsFile + ".lock")
	if err != nil {
		return "", err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	vault, identity, err := s.load()
	if err != nil {
		return "", err
	}
	if fields, ok := vault[service]; ok {
		if _, exists := fields[field]; exists && !replace {
			return "", &ConflictError{Message: fmt.Sprintf("secret %s/%s already exists", service, field)}
		}
	} else {
		vault[service] = map[string]string{}
	}
	vault[service][field] = string(value)
	return s.save(vault, identity)
}

func (s *Store) Get(service, field string) ([]byte, string, error) {
	if !ValidName(service) || field != "" && !ValidName(field) {
		return nil, "", &InvalidError{Message: "invalid secret service or field name"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vault, _, err := s.load()
	if err != nil {
		return nil, "", err
	}
	fields, ok := vault[service]
	if !ok {
		return nil, "", &NotFoundError{Message: fmt.Sprintf("secret service %q was not found", service)}
	}
	if field == "" {
		if len(fields) != 1 {
			return nil, "", &InvalidError{Message: fmt.Sprintf("secret service %q has %d fields; specify one", service, len(fields))}
		}
		for only := range fields {
			field = only
		}
	}
	value, ok := fields[field]
	if !ok {
		return nil, "", &NotFoundError{Message: fmt.Sprintf("secret %s/%s was not found", service, field)}
	}
	return []byte(value), field, nil
}

func (s *Store) Remove(service, field string) (backup string, err error) {
	if !ValidName(service) || field != "" && !ValidName(field) {
		return "", &InvalidError{Message: "invalid secret service or field name"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireSecretsFileLock(s.paths.SecretsFile + ".lock")
	if err != nil {
		return "", err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	vault, identity, err := s.load()
	if err != nil {
		return "", err
	}
	fields, ok := vault[service]
	if !ok {
		return "", &NotFoundError{Message: fmt.Sprintf("secret service %q was not found", service)}
	}
	if field == "" {
		delete(vault, service)
	} else {
		if _, ok := fields[field]; !ok {
			return "", &NotFoundError{Message: fmt.Sprintf("secret %s/%s was not found", service, field)}
		}
		delete(fields, field)
		if len(fields) == 0 {
			delete(vault, service)
		}
	}
	return s.save(vault, identity)
}

func (s *Store) Validate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	identityExists, err := pathExists(s.paths.AgeIdentityFile)
	if err != nil {
		return fmt.Errorf("inspect Workbench age identity: %w", err)
	}
	storeExists, err := pathExists(s.paths.SecretsFile)
	if err != nil {
		return fmt.Errorf("inspect Workbench secrets store: %w", err)
	}
	if !identityExists && !storeExists {
		return nil
	}
	if identityExists != storeExists {
		return errors.New("Workbench secrets identity and store must either both exist or both be absent")
	}
	_, _, err = s.load()
	return err
}

func (s *Store) load() (Vault, *age.X25519Identity, error) {
	identityBytes, err := readSecureFile(s.paths.AgeIdentityFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, errors.New("Workbench secrets are not initialized; run wb secrets init")
		}
		return nil, nil, fmt.Errorf("read Workbench age identity: %w", err)
	}
	if err := validateSecureDirectory(filepath.Dir(s.paths.AgeIdentityFile)); err != nil {
		return nil, nil, err
	}
	identity, err := parseX25519Identity(identityBytes)
	if err != nil {
		return nil, nil, &InvalidError{Message: "Workbench age identity is not a valid single X25519 identity"}
	}
	ciphertext, err := readSecureFile(s.paths.SecretsFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, errors.New("Workbench secrets store is missing")
		}
		return nil, nil, fmt.Errorf("read Workbench secrets store: %w", err)
	}
	vault, err := decrypt(ciphertext, identity)
	if err != nil {
		return nil, nil, err
	}
	return vault, identity, nil
}

func (s *Store) save(vault Vault, identity *age.X25519Identity) (string, error) {
	if err := ValidateVault(vault); err != nil {
		return "", err
	}
	ciphertext, err := encrypt(identity.Recipient(), vault)
	if err != nil {
		return "", err
	}
	backup, err := backupCiphertext(s.paths.SecretsFile, s.paths.BackupsDir)
	if err != nil {
		return "", err
	}
	if err := writeAtomicSecure(s.paths.SecretsFile, ciphertext); err != nil {
		return backup, s.rollback(backup, identity, fmt.Errorf("write secrets store: %w", err))
	}
	written, err := readSecureFile(s.paths.SecretsFile)
	if err != nil {
		return backup, s.rollback(backup, identity, fmt.Errorf("read written secrets store for verification: %w", err))
	}
	if err := s.verifyCiphertext(written, identity); err != nil {
		return backup, s.rollback(backup, identity, fmt.Errorf("verify written secrets store: %w", err))
	}
	return backup, nil
}

func verifyCiphertext(ciphertext []byte, identity *age.X25519Identity) error {
	_, err := decrypt(ciphertext, identity)
	return err
}

func (s *Store) rollback(backup string, identity *age.X25519Identity, cause error) error {
	if backup == "" {
		return cause
	}
	previous, err := readSecureFile(backup)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("read rollback ciphertext: %w", err))
	}
	if _, err := decrypt(previous, identity); err != nil {
		return errors.Join(cause, fmt.Errorf("validate rollback ciphertext: %w", err))
	}
	if err := writeAtomicSecure(s.paths.SecretsFile, previous); err != nil {
		return errors.Join(cause, fmt.Errorf("restore prior ciphertext: %w", err))
	}
	restored, err := readSecureFile(s.paths.SecretsFile)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("verify restored ciphertext: %w", err))
	}
	if _, err := decrypt(restored, identity); err != nil {
		return errors.Join(cause, fmt.Errorf("verify restored ciphertext: %w", err))
	}
	return fmt.Errorf("%w; prior ciphertext was restored", cause)
}

func encrypt(recipient age.Recipient, vault Vault) ([]byte, error) {
	plaintext, err := encodeVault(vault)
	if err != nil {
		return nil, err
	}
	var ciphertext bytes.Buffer
	writer, err := age.Encrypt(&ciphertext, recipient)
	if err != nil {
		return nil, fmt.Errorf("start age encryption: %w", err)
	}
	if _, err := writer.Write(plaintext); err != nil {
		return nil, fmt.Errorf("encrypt secrets store: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish age encryption: %w", err)
	}
	return ciphertext.Bytes(), nil
}

func decrypt(ciphertext []byte, identity age.Identity) (Vault, error) {
	plaintext, err := decryptPlaintext(ciphertext, identity)
	if err != nil {
		return nil, err
	}
	vault, err := decodeVault(plaintext)
	if err != nil {
		return nil, err
	}
	return vault, nil
}

func decryptPlaintext(ciphertext []byte, identity age.Identity) ([]byte, error) {
	reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, errors.New("decrypt secrets store: identity does not match or ciphertext is invalid")
	}
	plaintext, err := io.ReadAll(io.LimitReader(reader, maxPlaintextSize+1))
	if err != nil {
		return nil, errors.New("authenticate secrets store: ciphertext is invalid")
	}
	if len(plaintext) > maxPlaintextSize {
		return nil, &InvalidError{Message: "decrypted secrets store is too large"}
	}
	return plaintext, nil
}

func encodeVault(vault Vault) ([]byte, error) {
	if err := ValidateVault(vault); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(vault, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode secrets store: %w", err)
	}
	if len(data)+1 > maxPlaintextSize {
		return nil, &InvalidError{Message: "secrets store is too large"}
	}
	return append(data, '\n'), nil
}

func decodeVault(data []byte) (Vault, error) {
	vault, err := decodeVaultShape(data)
	if err != nil {
		return nil, err
	}
	if err := ValidateVault(vault); err != nil {
		return nil, err
	}
	return vault, nil
}

func decodeVaultShape(data []byte) (Vault, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, &InvalidError{Message: "decrypted secrets store is not valid legacy JSON"}
	}
	vault := Vault{}
	for decoder.More() {
		serviceToken, err := decoder.Token()
		service, ok := serviceToken.(string)
		if err != nil || !ok {
			return nil, &InvalidError{Message: "decrypted secrets store has an invalid service entry"}
		}
		if _, duplicate := vault[service]; duplicate {
			return nil, &InvalidError{Message: "decrypted secrets store contains a duplicate service name"}
		}
		object, err := decoder.Token()
		if err != nil || object != json.Delim('{') {
			return nil, &InvalidError{Message: "decrypted secrets store service values must be objects"}
		}
		fields := map[string]string{}
		for decoder.More() {
			fieldToken, err := decoder.Token()
			field, ok := fieldToken.(string)
			if err != nil || !ok {
				return nil, &InvalidError{Message: "decrypted secrets store has an invalid field entry"}
			}
			if _, duplicate := fields[field]; duplicate {
				return nil, &InvalidError{Message: "decrypted secrets store contains a duplicate field name"}
			}
			var value string
			if err := decoder.Decode(&value); err != nil {
				return nil, &InvalidError{Message: "decrypted secrets store field values must be strings"}
			}
			fields[field] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, &InvalidError{Message: "decrypted secrets store service object is incomplete"}
		}
		vault[service] = fields
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, &InvalidError{Message: "decrypted secrets store object is incomplete"}
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, &InvalidError{Message: "decrypted secrets store contains trailing JSON data"}
	}
	return vault, nil
}

func ValidateVault(vault Vault) error {
	for service, fields := range vault {
		if !ValidName(service) {
			return &InvalidError{Message: fmt.Sprintf("secret service name %q is invalid", service)}
		}
		if fields == nil {
			return &InvalidError{Message: fmt.Sprintf("secret service %q must be an object", service)}
		}
		for field := range fields {
			if !ValidName(field) {
				return &InvalidError{Message: fmt.Sprintf("secret field name %q is invalid", field)}
			}
		}
	}
	return nil
}

func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func parseX25519Identity(data []byte) (*age.X25519Identity, error) {
	var value string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if value != "" || !strings.HasPrefix(line, "AGE-SECRET-KEY-1") {
			return nil, errors.New("identity file must contain exactly one X25519 identity")
		}
		value = line
	}
	if value == "" {
		return nil, errors.New("identity file contains no X25519 identity")
	}
	return age.ParseX25519Identity(value)
}

func readSecureFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%s permissions must be 0600", path)
	}
	return os.ReadFile(path)
}

func writeAtomicSecure(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create secrets directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("secure secrets directory: %w", err)
		}
	}
	temporary, err := os.CreateTemp(directory, ".workbench-secrets-*.tmp")
	if err != nil {
		return fmt.Errorf("create encrypted temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write encrypted temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("flush encrypted temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close encrypted temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace encrypted file: %w", err)
	}
	return syncDirectory(directory)
}

func backupCiphertext(source, directory string) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create secrets backup directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return "", fmt.Errorf("secure secrets backup directory: %w", err)
		}
	}
	backup, err := storage.Backup(source, directory, "secrets.json.age")
	if err != nil {
		return "", err
	}
	if backup != "" {
		if err := syncDirectory(directory); err != nil {
			return backup, fmt.Errorf("flush secrets backup directory: %w", err)
		}
	}
	return backup, nil
}

func installPair(identityPath string, identity []byte, storePath string, ciphertext []byte) error {
	if filepath.Dir(identityPath) != filepath.Dir(storePath) {
		return errors.New("identity and secrets store must share a directory")
	}
	directory := filepath.Dir(identityPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return err
		}
	}
	identityTemp, err := createSyncedTemp(directory, ".workbench-identity-*.tmp", identity)
	if err != nil {
		return err
	}
	defer os.Remove(identityTemp)
	storeTemp, err := createSyncedTemp(directory, ".workbench-secrets-*.tmp", ciphertext)
	if err != nil {
		return err
	}
	defer os.Remove(storeTemp)
	parsed, err := parseX25519Identity(identity)
	if err != nil {
		return errors.New("refuse to install invalid X25519 identity")
	}
	if _, err := decrypt(ciphertext, parsed); err != nil {
		return errors.New("refuse to install secrets store that cannot be fully decrypted and validated")
	}
	identityExists, err := pathExists(identityPath)
	if err != nil {
		return err
	}
	storeExists, err := pathExists(storePath)
	if err != nil {
		return err
	}
	if identityExists || storeExists {
		return &ConflictError{Message: "Workbench secrets destination appeared during installation"}
	}
	if err := os.Rename(storeTemp, storePath); err != nil {
		return err
	}
	if err := os.Rename(identityTemp, identityPath); err != nil {
		_ = os.Remove(storePath)
		return err
	}
	return syncDirectory(directory)
}

func createSyncedTemp(directory, pattern string, data []byte) (string, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		if !ok {
			file.Close()
			os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func exists(path string) bool {
	exists, _ := pathExists(path)
	return exists
}

func validateSecureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Workbench secrets directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("Workbench secrets directory is not a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return errors.New("Workbench secrets directory permissions must be 0700")
	}
	return nil
}
