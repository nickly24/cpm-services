package exams

import (
	"encoding/hex"
	"strings"
)

func canonicalSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// Call after live validation while holding the exam and import-session locks.
// The proof covers all public rows, including excluded rows and current errors.
func verifyPinnedImport(revision int64, source string, expectedRevision int64, expectedSource, expectedValidation string, rows []ImportRow) error {
	if expectedRevision < 1 || !canonicalSHA256(expectedSource) || !canonicalSHA256(expectedValidation) {
		return Conflict("operation_request_mismatch")
	}
	if revision != expectedRevision {
		return Conflict("preview_modified")
	}
	if !canonicalSHA256(source) || source != expectedSource {
		return Conflict("import_source_modified")
	}
	hash, err := importValidationHash(rows)
	if err != nil {
		return err
	}
	if hash != expectedValidation {
		return Conflict("import_validation_modified")
	}
	return nil
}
