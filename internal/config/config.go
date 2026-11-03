package config

import (
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
	Environment               string
	HuaweiClientID            string
	HuaweiAccountClientSecret string
	AccountUnionIDPepper      []byte
	RedemptionCodePepper      []byte
	RedemptionCodesPath       string
	JWTSigningKey             []byte
	SessionTTL                time.Duration
}

// LoadStoreConfig validates all security-critical values before the API starts accepting traffic.
func LoadStoreConfig(environment string) (Config, error) {
	config := Config{
		Environment:               environment,
		HuaweiClientID:            strings.TrimSpace(os.Getenv("HUAWEI_CLIENT_ID")),
		HuaweiAccountClientSecret: strings.TrimSpace(os.Getenv("HUAWEI_ACCOUNT_CLIENT_SECRET")),
		AccountUnionIDPepper:      []byte(strings.TrimSpace(os.Getenv("ACCOUNT_UNION_ID_PEPPER"))),
		RedemptionCodePepper:      []byte(strings.TrimSpace(os.Getenv("REDEMPTION_CODE_PEPPER"))),
		RedemptionCodesPath:       strings.TrimSpace(os.Getenv("REDEMPTION_CODES_PATH")),
		JWTSigningKey:             []byte(strings.TrimSpace(os.Getenv("ONEBEAT_JWT_SIGNING_KEY"))),
		SessionTTL:                24 * time.Hour,
	}

	missing := make([]string, 0)
	for name, value := range map[string]string{
		"ACCOUNT_UNION_ID_PEPPER":      string(config.AccountUnionIDPepper),
		"ONEBEAT_JWT_SIGNING_KEY":      string(config.JWTSigningKey),
		"REDEMPTION_CODE_PEPPER":       string(config.RedemptionCodePepper),
		"REDEMPTION_CODES_PATH":        config.RedemptionCodesPath,
		"HUAWEI_CLIENT_ID":             config.HuaweiClientID,
		"HUAWEI_ACCOUNT_CLIENT_SECRET": config.HuaweiAccountClientSecret,
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
	if len(config.AccountUnionIDPepper) < 32 ||
		len(config.RedemptionCodePepper) < 32 || len(config.JWTSigningKey) < 32 {
		return Config{}, errors.New("store HMAC and JWT secrets must contain at least 32 characters")
	}

	return config, nil
}
