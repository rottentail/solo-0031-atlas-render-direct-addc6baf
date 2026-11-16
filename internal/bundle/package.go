package bundle

import (
	"atlas-render/internal/model"
	"atlas-render/internal/persist"
	"encoding/json"
)

// Package-level diagnostic codes. These describe why a submitted exchange
// package could not be accepted, relative to the package content itself.
const (
	CodeFormat   = "package.format"
	CodeDigest   = "package.digest"
	CodeDecode   = "package.decode"
	CodeTooLarge = "package.size"
	CodeSnapshot = "package.snapshot"
)

type Package struct {
	Format   string         `json:"format"`
	Version  int            `json:"version"`
	Snapshot model.Snapshot `json:"snapshot"`
	Digest   string         `json:"digest"`
}

func Export(s model.Snapshot) Package {
	p := Package{Format: "atlas-scene", Version: 1, Snapshot: s}
	p.Digest = model.CanonicalDigest(p.Snapshot)
	return p
}

// packageSubject binds package-shape diagnostics to the embedded document when
// there is one. The content digest lets a client compare the package basis with
// a direct save of the same document.
func packageSubject(p Package) model.DiagnosticSubject {
	return model.DiagnosticSubject{
		Kind:          model.SubjectPackage,
		Origin:        "import",
		ContentDigest: model.DocumentDigest(p.Snapshot.Document),
		SnapshotID:    p.Snapshot.ID,
	}
}

func Validate(p Package) error {
	if p.Format != "atlas-scene" || p.Version != 1 {
		return model.AttachSubject(model.Diagnosed("unsupported scene package", model.Diagnostic{
			Code: CodeFormat, Severity: "error",
			Message:  "package format must be atlas-scene version 1",
			Field:    "format,version",
			Evidence: map[string]any{"format": p.Format, "version": p.Version},
		}), packageSubject(p))
	}
	if model.CanonicalDigest(p.Snapshot) != p.Digest {
		return model.AttachSubject(model.Diagnosed("package checksum mismatch", model.Diagnostic{
			Code: CodeDigest, Severity: "error",
			Message:  "outer package digest does not match the embedded snapshot",
			Field:    "digest",
			Evidence: map[string]any{"declared": p.Digest, "computed": model.CanonicalDigest(p.Snapshot)},
		}), packageSubject(p))
	}
	if e := persist.ValidateSnapshot(p.Snapshot); e != nil {
		// Reuse the shared document/snapshot diagnostics. Document-scoped
		// diagnostics are bound to the package document; the import service
		// retargets only the origin and destination so save and import agree on
		// the same content basis. Asset-scoped diagnostics keep naming the
		// uploaded bytes that were actually decoded.
		if report := model.ReportOf(e); report != nil {
			if report.Subject.Kind == model.SubjectDocument {
				return model.RebindSubject(e, model.DocumentSubject("import", "", p.Snapshot.Document))
			}
			return e
		}
		return model.AttachSubject(model.Diagnosed("scene package snapshot is invalid", model.Diagnostic{
			Code: CodeSnapshot, Severity: "error", Message: e.Error(),
			Field: "snapshot", Evidence: map[string]any{"detail": e.Error()},
		}), packageSubject(p))
	}
	return nil
}
func Decode(data []byte) (Package, error) {
	var p Package
	if len(data) > 12<<20 {
		return p, model.Diagnosed("package exceeds 12MiB", model.Diagnostic{
			Code: CodeTooLarge, Severity: "error",
			Message: "package body must not exceed 12MiB", Field: "package",
			Evidence: map[string]any{"bytes": len(data), "limit": 12 << 20},
		})
	}
	if e := json.Unmarshal(data, &p); e != nil {
		return p, model.Diagnosed("decode package: "+e.Error(), model.Diagnostic{
			Code: CodeDecode, Severity: "error",
			Message: "package is not valid JSON: " + e.Error(), Field: "package",
		})
	}
	return p, Validate(p)
}
