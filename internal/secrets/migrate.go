package secrets

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jisung9870/workbench/internal/config"
)

type LegacyPaths struct {
	Identity string
	Store    string
}

type MigrationPlan struct {
	SourceIdentity       string   `json:"source_identity"`
	SourceStore          string   `json:"source_store"`
	IdentityType         string   `json:"identity_type,omitempty"`
	IdentityMode         string   `json:"identity_mode,omitempty"`
	StoreMode            string   `json:"store_mode,omitempty"`
	IdentityHealthy      bool     `json:"identity_healthy"`
	StoreHealthy         bool     `json:"store_healthy"`
	DecryptValid         bool     `json:"decrypt_valid"`
	SchemaValid          bool     `json:"schema_valid"`
	NamesValid           bool     `json:"names_valid"`
	ServiceCount         int      `json:"service_count"`
	FieldCount           int      `json:"field_count"`
	DestinationAvailable bool     `json:"destination_available"`
	CanApply             bool     `json:"can_apply"`
	Issues               []string `json:"issues"`
	signature            [32]byte
}

func DefaultLegacyPaths(paths config.Paths) LegacyPaths {
	binbox := filepath.Join(filepath.Dir(paths.ConfigDir), "binbox")
	identity := os.Getenv("BINBOX_AGE_KEY")
	if identity == "" {
		identity = filepath.Join(binbox, "age.key")
	}
	store := os.Getenv("BINBOX_SECRETS_FILE")
	if store == "" {
		store = filepath.Join(binbox, "secrets.json.age")
	}
	return LegacyPaths{Identity: identity, Store: store}
}

func PlanMigration(source LegacyPaths, destination config.Paths) (MigrationPlan, error) {
	identityPath, err := filepath.Abs(source.Identity)
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("resolve legacy identity path: %w", err)
	}
	storePath, err := filepath.Abs(source.Store)
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("resolve legacy store path: %w", err)
	}
	plan := MigrationPlan{SourceIdentity: identityPath, SourceStore: storePath, Issues: []string{}}
	identityBytes, identityInfo, err := readMigrationSource(identityPath)
	if err != nil {
		plan.Issues = append(plan.Issues, migrationFileIssue("identity", err))
	} else {
		plan.IdentityMode = modeString(identityInfo)
		plan.IdentityHealthy = secureMode(identityInfo)
		if !plan.IdentityHealthy {
			plan.Issues = append(plan.Issues, "legacy identity permissions must be 0600")
		}
	}
	storeBytes, storeInfo, err := readMigrationSource(storePath)
	if err != nil {
		plan.Issues = append(plan.Issues, migrationFileIssue("store", err))
	} else {
		plan.StoreMode = modeString(storeInfo)
		plan.StoreHealthy = secureMode(storeInfo)
		if !plan.StoreHealthy {
			plan.Issues = append(plan.Issues, "legacy store permissions must be 0600")
		}
	}
	if len(identityBytes) > 0 {
		identity, parseErr := parseX25519Identity(identityBytes)
		if parseErr != nil {
			plan.Issues = append(plan.Issues, "legacy identity must contain exactly one unencrypted X25519 identity")
		} else {
			plan.IdentityType = "x25519"
			if len(storeBytes) > 0 {
				plaintext, decryptErr := decryptPlaintext(storeBytes, identity)
				if decryptErr != nil {
					plan.Issues = append(plan.Issues, "legacy store cannot be fully decrypted and authenticated with the legacy identity")
				} else {
					plan.DecryptValid = true
					vault, decodeErr := decodeVaultShape(plaintext)
					if decodeErr != nil {
						plan.Issues = append(plan.Issues, "legacy plaintext is not the supported service/field string JSON schema")
					} else {
						plan.SchemaValid = true
						if nameErr := ValidateVault(vault); nameErr != nil {
							plan.Issues = append(plan.Issues, "legacy plaintext contains an invalid service or field name")
						} else {
							plan.NamesValid = true
							plan.ServiceCount = len(vault)
							for _, fields := range vault {
								plan.FieldCount += len(fields)
							}
						}
					}
				}
			}
		}
	}
	plan.DestinationAvailable = !exists(destination.AgeIdentityFile) && !exists(destination.SecretsFile)
	if !plan.DestinationAvailable {
		plan.Issues = append(plan.Issues, "Workbench secrets destination already exists")
	}
	plan.CanApply = plan.IdentityHealthy && plan.StoreHealthy && plan.IdentityType == "x25519" && plan.DecryptValid && plan.SchemaValid && plan.NamesValid && plan.DestinationAvailable
	plan.signature = migrationSignature(identityBytes, storeBytes, destination)
	return plan, nil
}

func ApplyMigration(checked MigrationPlan, destination config.Paths) (err error) {
	release, err := acquireSecretsFileLock(destination.SecretsFile + ".lock")
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	current, err := PlanMigration(LegacyPaths{Identity: checked.SourceIdentity, Store: checked.SourceStore}, destination)
	if err != nil {
		return err
	}
	if current.signature != checked.signature {
		return &ConflictError{Message: "legacy secrets source or Workbench destination changed after migration check"}
	}
	if !current.CanApply {
		return &ConflictError{Message: "legacy secrets migration is blocked; no destination files were changed"}
	}
	identity, err := os.ReadFile(current.SourceIdentity)
	if err != nil {
		return fmt.Errorf("reread legacy identity: %w", err)
	}
	ciphertext, err := os.ReadFile(current.SourceStore)
	if err != nil {
		return fmt.Errorf("reread legacy secrets store: %w", err)
	}
	if migrationSignature(identity, ciphertext, destination) != current.signature {
		return &ConflictError{Message: "legacy secrets source or Workbench destination changed during migration"}
	}
	if err := installPair(destination.AgeIdentityFile, identity, destination.SecretsFile, ciphertext); err != nil {
		return err
	}
	if err := NewStore(destination).Validate(); err != nil {
		_ = os.Remove(destination.AgeIdentityFile)
		_ = os.Remove(destination.SecretsFile)
		return fmt.Errorf("verify migrated Workbench secrets: %w", err)
	}
	return nil
}

func readMigrationSource(path string) ([]byte, fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, info, errors.New("not a regular file")
	}
	data, err := os.ReadFile(path)
	return data, info, err
}

func secureMode(info fs.FileInfo) bool {
	return runtime.GOOS == "windows" || info.Mode().Perm() == 0o600
}

func modeString(info fs.FileInfo) string {
	if runtime.GOOS == "windows" {
		return "platform-acl"
	}
	return fmt.Sprintf("%04o", info.Mode().Perm())
}

func migrationFileIssue(kind string, err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "legacy " + kind + " file is missing"
	}
	return "legacy " + kind + " file is not a readable regular file"
}

func migrationSignature(identity, store []byte, destination config.Paths) [32]byte {
	buffer := append([]byte{}, identity...)
	buffer = append(buffer, 0)
	buffer = append(buffer, store...)
	buffer = append(buffer, 0)
	if exists(destination.AgeIdentityFile) {
		buffer = append(buffer, 'i')
	}
	if exists(destination.SecretsFile) {
		buffer = append(buffer, 's')
	}
	return sha256.Sum256(buffer)
}
