package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	HuaweiApplicationID = "6917617181108973504"
	HuaweiPackageName   = "com.onebeat.app"
)

// Config contains the secrets and environment switches required by the store API.
type Config struct {
	Environment                string
	HuaweiClientID             string
	HuaweiAccountClientSecret  string
	HuaweiIAPEnvironment       string
	HuaweiIAPPrivateKey        string
	HuaweiIAPKeyID             string
	HuaweiIAPIssuerID          string
	AccountUnionIDPepper       []byte
	PurchaseBindingSecret      []byte
	PurchaseTokenEncryptionKey []byte
	JWTSigningKey              []byte
	SessionTTL                 time.Duration
}

// LoadStoreConfig validates all security-critical values before the API starts accepting traffic.
func LoadStoreConfig(environment string) (Config, error) {
	tokenKey, err := decodeEncryptionKey(os.Getenv("PURCHASE_TOKEN_ENCRYPTION_KEY"))
	if err != nil {
		return Config{}, err
	}

	config := Config{
		Environment:                environment,
		HuaweiClientID:             strings.TrimSpace(os.Getenv("HUAWEI_CLIENT_ID")),
		HuaweiAccountClientSecret:  strings.TrimSpace(os.Getenv("HUAWEI_ACCOUNT_CLIENT_SECRET")),
		HuaweiIAPEnvironment:       strings.ToLower(strings.TrimSpace(os.Getenv("HUAWEI_IAP_ENVIRONMENT"))),
		HuaweiIAPPrivateKey:        strings.ReplaceAll(os.Getenv("HUAWEI_IAP_PRIVATE_KEY"), `\n`, "\n"),
		HuaweiIAPKeyID:             strings.TrimSpace(os.Getenv("HUAWEI_IAP_KEY_ID")),
		HuaweiIAPIssuerID:          strings.TrimSpace(os.Getenv("HUAWEI_IAP_ISSUER_ID")),
		AccountUnionIDPepper:       []byte(strings.TrimSpace(os.Getenv("ACCOUNT_UNION_ID_PEPPER"))),
		PurchaseBindingSecret:      []byte(strings.TrimSpace(os.Getenv("PURCHASE_BINDING_SECRET"))),
		PurchaseTokenEncryptionKey: tokenKey,
		JWTSigningKey:              []byte(strings.TrimSpace(os.Getenv("ONEBEAT_JWT_SIGNING_KEY"))),
		SessionTTL:                 24 * time.Hour,
	}

	missing := make([]string, 0)
	for name, value := range map[string]string{
		"ACCOUNT_UNION_ID_PEPPER":      string(config.AccountUnionIDPepper),
		"PURCHASE_BINDING_SECRET":      string(config.PurchaseBindingSecret),
		"ONEBEAT_JWT_SIGNING_KEY":      string(config.JWTSigningKey),
		"HUAWEI_CLIENT_ID":             config.HuaweiClientID,
		"HUAWEI_ACCOUNT_CLIENT_SECRET": config.HuaweiAccountClientSecret,
		"HUAWEI_IAP_PRIVATE_KEY":       config.HuaweiIAPPrivateKey,
		"HUAWEI_IAP_KEY_ID":            config.HuaweiIAPKeyID,
		"HUAWEI_IAP_ISSUER_ID":         config.HuaweiIAPIssuerID,
		"HUAWEI_IAP_ENVIRONMENT":       config.HuaweiIAPEnvironment,
	} {
		if strings.TrimSpace(value) == "" || strings.HasPrefix(value, "REPLACE_") {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing store secrets: %s", strings.Join(missing, ", "))
	}
	if config.HuaweiClientID != HuaweiApplicationID {
		return Config{}, fmt.Errorf("HUAWEI_CLIENT_ID must match application ID %s", HuaweiApplicationID)
	}
	if config.HuaweiIAPEnvironment != "sandbox" && config.HuaweiIAPEnvironment != "production" {
		return Config{}, errors.New("HUAWEI_IAP_ENVIRONMENT must be sandbox or production")
	}
	if len(config.AccountUnionIDPepper) < 32 || len(config.PurchaseBindingSecret) < 32 || len(config.JWTSigningKey) < 32 {
		return Config{}, errors.New("store HMAC and JWT secrets must contain at least 32 characters")
	}

	return config, nil
}

func decodeEncryptionKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "REPLACE_") {
		return nil, errors.New("PURCHASE_TOKEN_ENCRYPTION_KEY is required")
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("PURCHASE_TOKEN_ENCRYPTION_KEY must be 64 hexadecimal characters")
	}
	return decoded, nil
}
