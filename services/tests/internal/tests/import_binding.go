package tests

import (
	"net/http"
	"strconv"
)

func checkJSONImportBinding(r *http.Request, a Actor, in JSONImportPreviewDTO, command, requestHash string) error {
	ctx := importOperationContext(r)
	if ctx == nil {
		return nil
	}
	b := ctx.Authorization.Binding
	if in.OrganizationID != a.OrganizationID || a.DivisionID == nil || in.DivisionID != *a.DivisionID || b.Target.DirectionID == nil || *b.Target.DirectionID != strconv.FormatInt(in.DirectionID, 10) || b.OwnerProof.InputSHA256 == nil || *b.OwnerProof.InputSHA256 != externalImportHash(in) {
		return conflict("operation_request_mismatch")
	}
	if ctx.Wire.Stage != "preview" && (command != b.NativeCommandKey || requestHash != b.OwnerRequestSHA256) {
		return conflict("operation_request_mismatch")
	}
	return nil
}

func checkExternalImportBinding(r *http.Request, a Actor, in ExternalImportValidateDTO, command, requestHash string) error {
	ctx := importOperationContext(r)
	if ctx == nil {
		return nil
	}
	b := ctx.Authorization.Binding
	if in.OrganizationID != a.OrganizationID || a.DivisionID == nil || in.DivisionID != *a.DivisionID || b.Target.TestID == nil || in.TestID != *b.Target.TestID || b.TargetRevision == nil || strconv.FormatInt(in.ExpectedTestRevision, 10) != *b.TargetRevision || b.OwnerProof.InputSHA256 == nil || *b.OwnerProof.InputSHA256 != externalImportHash(in) {
		return conflict("operation_request_mismatch")
	}
	if ctx.Wire.Stage != "preview" && (command != b.NativeCommandKey || requestHash != b.OwnerRequestSHA256) {
		return conflict("operation_request_mismatch")
	}
	return nil
}

func checkImportOwnerProof(r *http.Request, proof *ImportOwnerProof) error {
	ctx := importOperationContext(r)
	if ctx == nil {
		return nil
	}
	want := ctx.Authorization.Binding.OwnerProof
	if proof == nil || proof.SchemaVersion != 1 || proof.Scheme != want.Scheme || want.InputSHA256 == nil || proof.InputSHA256 != *want.InputSHA256 || proof.ValidationSHA256 != want.ValidationSHA256 {
		return conflict("operation_owner_proof_changed")
	}
	if (proof.MappingSHA256 == nil) != (want.MappingSHA256 == nil) || proof.MappingSHA256 != nil && *proof.MappingSHA256 != *want.MappingSHA256 {
		return conflict("operation_owner_proof_changed")
	}
	return nil
}

func checkExternalCommandBinding(r *http.Request, in ExternalImportCommitDTO) error {
	ctx := importOperationContext(r)
	if ctx == nil {
		return nil
	}
	b := ctx.Authorization.Binding
	if in.UploadID != b.UploadID || in.UploadSessionID != b.UploadSessionID || strconv.FormatInt(in.UploadPreviewRevision, 10) != b.UploadPreviewRevision {
		return conflict("operation_request_mismatch")
	}
	return nil
}
