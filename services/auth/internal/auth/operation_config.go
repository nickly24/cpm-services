package auth

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type OperationConfig struct {
	Enabled   bool
	IssuerKey string
	OwnerKeys map[string]string
	TTL       time.Duration
}

func loadOperationConfig(internalKey string) (OperationConfig, error) {
	c := OperationConfig{IssuerKey: os.Getenv("AUTH_OPERATIONS_ISSUER_KEY"), OwnerKeys: map[string]string{}, TTL: 6 * time.Hour}
	for _, owner := range []string{"core", "cards", "tests", "exams"} {
		c.OwnerKeys[owner] = os.Getenv("AUTH_OPERATIONS_" + strings.ToUpper(owner) + "_KEY")
	}
	flag := os.Getenv("AUTH_OPERATIONS_ENABLED")
	if flag != "" && flag != "0" && flag != "1" {
		return c, errors.New("invalid AUTH_OPERATIONS_ENABLED")
	}
	c.Enabled = flag == "1"
	if value := os.Getenv("AUTH_OPERATIONS_TTL_SECONDS"); value != "" {
		n, e := strconv.ParseInt(value, 10, 64)
		if e != nil || n < 300 || n > 86400 {
			return c, errors.New("invalid AUTH_OPERATIONS_TTL_SECONDS")
		}
		c.TTL = time.Duration(n) * time.Second
	}
	if e := c.validate(internalKey); e != nil {
		return c, e
	}
	return c, nil
}

func (c OperationConfig) validate(internalKey string) error {
	keys := []string{c.IssuerKey}
	for _, owner := range []string{"core", "cards", "tests", "exams"} {
		keys = append(keys, c.OwnerKeys[owner])
	}
	anyKey := false
	for _, key := range keys {
		anyKey = anyKey || key != ""
	}
	if !c.Enabled {
		if anyKey {
			return errors.New("operation credentials require AUTH_OPERATIONS_ENABLED=1")
		}
		return nil
	}
	if c.TTL < 5*time.Minute || c.TTL > 24*time.Hour {
		return errors.New("invalid operation TTL")
	}
	seen := map[string]bool{internalKey: true}
	for _, key := range keys {
		if len(key) < 32 || len(key) > 256 || key != strings.TrimSpace(key) || seen[key] {
			return errors.New("operation credentials must be complete and distinct")
		}
		seen[key] = true
	}
	if len(c.OwnerKeys) != 4 {
		return errors.New("invalid operation audience configuration")
	}
	return nil
}
