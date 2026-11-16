package persist

import (
	"atlas-render/internal/codec"
	"atlas-render/internal/model"
	"atlas-render/internal/scene"
	"fmt"
)

type validation struct {
	record  string
	message string
}

func (v *validation) Error() string { return v.message }

// errorf keeps the persisted-state validation contract: startup rejection
// messages are unchanged from the baseline so existing state files and
// operators see the same text.
func errorf(format string, args ...any) error {
	return &validation{message: fmt.Sprintf(format, args...)}
}
func recordError(record, format string, args ...any) error {
	return &validation{record: record, message: fmt.Sprintf(format, args...)}
}

// Diagnose converts a persisted-state validation failure into the opt-in
// structured form. It is only used on the request path (package import), never
// when opening an existing state file, so historical data is not rewritten and
// startup messages stay identical.
func Diagnose(err error) error {
	if v, ok := err.(*validation); ok && v.record != "" {
		return model.Diagnosed(v.message, model.Diagnostic{
			Code: "storage.invalidRecord", Severity: "error", Message: v.message,
			Evidence: map[string]any{"record": v.record},
		})
	}
	return err
}

func Validate(s model.State) error {
	if s.Version != model.FormatVersion {
		return errorf("unsupported state version %d", s.Version)
	}
	if s.Scenes == nil || s.Assets == nil || s.Snapshots == nil || s.Jobs == nil || s.Artifacts == nil || s.Receipts == nil {
		return errorf("state maps must be present")
	}
	for id, v := range s.Scenes {
		if id != v.ID || !scene.ValidID(id) || v.Revision < 1 || (v.State != "active" && v.State != "archived") {
			return recordError("scene:"+id, "invalid scene record %s", id)
		}
		if e := scene.Validate(v.Document); e != nil {
			return e
		}
	}
	for id, v := range s.Assets {
		if id != v.ID || !scene.ValidID(id) || (v.State != "available" && v.State != "retired") {
			return recordError("asset:"+id, "invalid asset record %s", id)
		}
		if e := ValidateAsset(v); e != nil {
			return e
		}
	}
	for id, v := range s.Snapshots {
		if id != v.ID || (v.State != "active" && v.State != "revoked") {
			return recordError("snapshot:"+id, "invalid snapshot record %s", id)
		}
		if _, ok := s.Scenes[v.SceneID]; !ok {
			return recordError("snapshot:"+id, "orphan snapshot %s", id)
		}
		if e := ValidateSnapshot(v); e != nil {
			return e
		}
	}
	for id, j := range s.Jobs {
		if id != j.ID || j.SnapshotID != j.Payload.ID || j.Scale < 1 || j.Scale > 2 || j.Attempt < 1 {
			return recordError("job:"+id, "invalid job %s", id)
		}
		if _, ok := s.Snapshots[j.SnapshotID]; !ok {
			return recordError("job:"+id, "orphan job %s", id)
		}
		switch j.State {
		case "queued", "running", "failed", "cancelled":
			if j.ArtifactID != "" {
				return recordError("job:"+id, "unfinished job references artifact")
			}
		case "succeeded":
			a, ok := s.Artifacts[j.ArtifactID]
			if !ok || a.JobID != id {
				return recordError("job:"+id, "job result missing")
			}
		default:
			return recordError("job:"+id, "invalid job state")
		}
		if e := ValidateSnapshot(j.Payload); e != nil {
			return e
		}
	}
	for id, a := range s.Artifacts {
		j, ok := s.Jobs[a.JobID]
		if id != a.ID || !ok || j.ArtifactID != id || j.State != "succeeded" {
			return recordError("artifact:"+id, "orphan artifact %s", id)
		}
		switch a.State {
		case "live":
			if model.Digest(a.PNG) != a.Digest {
				return recordError("artifact:"+id, "artifact digest mismatch")
			}
		case "purged":
			if len(a.PNG) != 0 {
				return recordError("artifact:"+id, "purged artifact retains bytes")
			}
		default:
			return recordError("artifact:"+id, "invalid artifact state")
		}
	}
	for key, r := range s.Receipts {
		if key != r.Key || r.Fingerprint == "" {
			return recordError("receipt:"+key, "invalid receipt")
		}
		if _, ok := s.Jobs[r.JobID]; !ok {
			return recordError("receipt:"+key, "receipt result missing")
		}
	}
	return nil
}
func ValidateAsset(a model.Asset) error {
	if model.Digest(a.PNG) != a.Digest {
		return model.Invalid("asset digest mismatch")
	}
	img, e := codec.Decode(a.PNG)
	if e != nil {
		return e
	}
	if img.Bounds().Dx() != a.Width || img.Bounds().Dy() != a.Height {
		return model.Invalid("asset dimensions mismatch")
	}
	return nil
}
func ValidateSnapshot(v model.Snapshot) error {
	if e := scene.Validate(v.Document); e != nil {
		// Document diagnostics stay bound to the document they were computed
		// from; the caller may retarget the origin.
		return model.AttachSubject(e, model.DocumentSubject("snapshot", v.SceneID, v.Document))
	}
	if model.SnapshotDigest(v) != v.Digest {
		return model.Diagnosed("snapshot digest mismatch", model.Diagnostic{
			Code: "snapshot.digest", Severity: "error",
			Message: "snapshot digest does not match its document and asset closure",
			Field:   "digest",
		})
	}
	refs := scene.References(v.Document)
	if len(refs) != len(v.Assets) {
		return model.Diagnosed("snapshot asset closure mismatch", model.Diagnostic{
			Code: "snapshot.assetClosure", Severity: "error",
			Message:  "snapshot asset closure does not match the document image references",
			Field:    "assets",
			Evidence: map[string]any{"referenced": len(refs), "captured": len(v.Assets)},
		})
	}
	for _, id := range refs {
		a, ok := v.Assets[id]
		if !ok || id != a.ID {
			return model.Diagnosed("snapshot asset missing", model.Diagnostic{
				Code: "snapshot.assetMissing", Severity: "error",
				Message: "snapshot is missing the bytes for referenced asset " + id,
				Field:   "assets." + id, Evidence: map[string]any{"assetId": id},
			})
		}
		if e := ValidateAsset(a); e != nil {
			return e
		}
	}
	return nil
}
