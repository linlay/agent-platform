package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/catalogorder"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/server"
)

type desktopOwnerMigrationReceipt struct {
	Subject     string           `json:"subject"`
	DeviceID    string           `json:"deviceId"`
	ChatsRoot   string           `json:"chatsRoot"`
	CreatedAt   string           `json:"createdAt"`
	CompletedAt string           `json:"completedAt,omitempty"`
	Changed     map[string]int64 `json:"changed,omitempty"`
	PinsCopied  map[string]bool  `json:"pinsCopied,omitempty"`
}

func runDesktopOwnerMigration(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("desktop-owner-migrate", flag.ContinueOnError)
	configDir := flags.String("config-dir", "", "service configuration root")
	identityFile := flags.String("identity-file", "", "absolute Desktop verified identity file override")
	confirmed := flags.Bool("confirm-offline", false, "confirm Platform is stopped and explicitly adopt legacy Desktop data for the current verified user")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*confirmed {
		return fmt.Errorf("usage: desktop-owner-migrate --config-dir <service-config> [--identity-file <absolute-path>] --confirm-offline < current-platform-jwt")
	}
	cfg, err := config.Load(config.LoadOptions{ConfigDir: *configDir, IdentityFile: *identityFile, RuntimeMode: string(config.RuntimeModeDesktop)})
	if err != nil {
		return err
	}
	tokenBytes, err := io.ReadAll(io.LimitReader(in, 65537))
	if err != nil || len(tokenBytes) > 65536 {
		return fmt.Errorf("cannot read bounded Platform JWT from stdin")
	}
	subject, device, err := verifyDesktopMigrationIdentity(cfg, strings.TrimSpace(string(tokenBytes)))
	if err != nil {
		return err
	}
	for _, host := range []string{"127.0.0.1", "::1"} {
		connection, err := net.DialTimeout("tcp", net.JoinHostPort(host, cfg.Server.Port), 300*time.Millisecond)
		if err == nil {
			connection.Close()
			return fmt.Errorf("stop Platform before migrating legacy Desktop ownership")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := migrateDesktopOwner(ctx, cfg, subject, device)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}

func verifyDesktopMigrationIdentity(cfg config.Config, token string) (string, string, error) {
	if cfg.RuntimeMode != config.RuntimeModeDesktop || !cfg.Auth.Enabled || cfg.Auth.LocalPublicKeyFile == "" || cfg.IdentityFile == "" {
		return "", "", fmt.Errorf("migration requires Desktop mode, local JWT verification and a trusted identity file")
	}
	principal, err := server.NewJWTVerifier(cfg.Auth).Verify(token)
	if err != nil {
		return "", "", fmt.Errorf("current Platform JWT did not pass local verification")
	}
	if !regexp.MustCompile(`^desktop-user:[a-f0-9]{64}$`).MatchString(principal.Subject) {
		return "", "", fmt.Errorf("a verified Desktop user token is required")
	}
	device, _ := principal.Claims["device_id"].(string)
	expiration, _ := principal.Claims["exp"].(float64)
	if strings.TrimSpace(device) == "" || expiration <= float64(time.Now().Unix()) {
		return "", "", fmt.Errorf("current Desktop device and unexpired identity are required")
	}
	identity, err := agentconfig.ReadIdentityEnvironment(cfg.IdentityFile)
	if err != nil || identity[agentconfig.EnvAccessToken] == "" || !connector.IdentityMatchesOwner(principal.Subject, identity[agentconfig.EnvAccessToken]) {
		return "", "", fmt.Errorf("Platform subject does not match Desktop's currently verified canonical identity")
	}
	return principal.Subject, device, nil
}

func migrateDesktopOwner(ctx context.Context, cfg config.Config, subject, device string) (desktopOwnerMigrationReceipt, error) {
	var receipt desktopOwnerMigrationReceipt
	if !regexp.MustCompile(`^desktop-user:[a-f0-9]{64}$`).MatchString(subject) || strings.TrimSpace(device) == "" || !filepath.IsAbs(cfg.Paths.ChatsDir) || !filepath.IsAbs(cfg.Paths.EffectiveStateDir()) {
		return receipt, fmt.Errorf("verified identity and absolute configured data roots are required")
	}
	root := filepath.Join(cfg.Paths.EffectiveStateDir(), "desktop-owner-migration")
	if err := os.MkdirAll(root, 0700); err != nil {
		return receipt, err
	}
	release, err := connector.AcquireOperation(root, "legacy-desktop-owner")
	if err != nil {
		return receipt, err
	}
	defer release()
	file := filepath.Join(root, "owner.json")
	if info, err := os.Lstat(file); err == nil && !info.Mode().IsRegular() {
		return receipt, fmt.Errorf("migration receipt must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return receipt, err
	}
	data, err := os.ReadFile(file)
	if err == nil {
		if json.Unmarshal(data, &receipt) != nil || receipt.Subject != subject || receipt.DeviceID != device || receipt.ChatsRoot != cfg.Paths.ChatsDir {
			return receipt, fmt.Errorf("legacy Desktop ownership was already reserved for a different verified identity or data root")
		}
		if receipt.CompletedAt != "" {
			return receipt, nil
		}
	} else if os.IsNotExist(err) {
		receipt = desktopOwnerMigrationReceipt{Subject: subject, DeviceID: device, ChatsRoot: cfg.Paths.ChatsDir, CreatedAt: time.Now().UTC().Format(time.RFC3339), Changed: map[string]int64{}, PinsCopied: map[string]bool{}}
		if err := writeDesktopMigrationReceipt(file, receipt); err != nil {
			return receipt, err
		}
	} else {
		return receipt, err
	}
	changed, err := chat.MigrateLegacyDesktopSource(ctx, cfg.Paths.ChatsDir, filepath.Join(root, "backups"), subject)
	if err != nil {
		return receipt, err
	}
	if receipt.Changed == nil {
		receipt.Changed = map[string]int64{}
	}
	for key, count := range changed {
		receipt.Changed[key] += count
	}
	if receipt.PinsCopied == nil {
		receipt.PinsCopied = map[string]bool{}
	}
	for name, center := range map[string]string{"skills": cfg.Paths.SkillsCenterDir, "connectors": cfg.Paths.EffectiveConnectorsCenterDir()} {
		copied, err := catalogorder.CopyLegacyDesktopPins(center, filepath.Join(root, "backups", name+"-order.json"), subject)
		if err != nil {
			return receipt, err
		}
		receipt.PinsCopied[name] = receipt.PinsCopied[name] || copied
	}
	receipt.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeDesktopMigrationReceipt(file, receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func writeDesktopMigrationReceipt(path string, value desktopOwnerMigrationReceipt) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".owner-receipt-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
