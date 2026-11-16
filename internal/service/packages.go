package service

import (
	"atlas-render/internal/bundle"
	"atlas-render/internal/model"
	"atlas-render/internal/persist"
	"atlas-render/internal/scene"
	"time"
)

func (s *Service) Export(id string) (bundle.Package, error) {
	v, ok := s.Store.Read().Snapshots[id]
	if !ok {
		return bundle.Package{}, model.Missing("snapshot not found")
	}
	return bundle.Export(v), nil
}

func (s *Service) Import(id, name string, p bundle.Package) (model.Scene, error) {
	var out model.Scene
	if !scene.ValidID(id) || len(name) < 1 || len(name) > 160 {
		return out, model.Invalid("invalid import destination")
	}
	// Validate first so failures bind to the package content that was actually
	// checked. Document-scoped diagnostics are retargeted to the destination
	// subject used by scene saving, so identical content yields identical
	// diagnostics from direct save and from package import. Asset-scoped
	// diagnostics keep naming the uploaded bytes that were decoded.
	if e := bundle.Validate(p); e != nil {
		return out, bindImport(e, id, p)
	}
	e := s.Store.Update(func(st *model.State) error {
		subject := model.DocumentSubject("import", id, p.Snapshot.Document)
		if _, ok := st.Scenes[id]; ok {
			return model.Conflict("destination scene already exists")
		}
		for aid, a := range p.Snapshot.Assets {
			existing, ok := st.Assets[aid]
			if ok && (existing.Digest != a.Digest || existing.State != "available") {
				locations := scene.LocationsForAsset(p.Snapshot.Document, aid)
				main := model.LayerLocator{Path: "/layers"}
				related := make([]model.LayerLocator, 0, len(locations))
				for _, loc := range locations {
					if len(related) == 0 {
						main = loc
					}
					related = append(related, loc)
				}
				if len(related) == 0 {
					related = nil
				}
				// The code names the underlying cause so a direct save of the
				// same document produces the same diagnostic; the legacy
				// message stays the import-specific conflict text.
				code := "asset.unavailable"
				state := "retired"
				if existing.Digest != a.Digest {
					code = "package.assetConflict"
					state = "contentMismatch"
				}
				return model.AttachSubject(model.DiagnosedStatus(409, "conflict", "destination asset conflicts with package", model.Diagnostic{
					Code: code, Severity: "error",
					Message: "asset " + aid + " cannot be imported: the destination already holds " + state + " content for this id, so the package layers cannot resolve unchanged",
					Layer:   &main, Field: "assetId", Related: related,
					Evidence: map[string]any{
						"assetId": aid, "state": existing.State, "reason": state, "referenceCount": len(locations),
						"packageDigest": a.Digest, "destinationDigest": existing.Digest,
					},
				}), subject)
			}
			if !ok {
				a.State = "available"
				st.Assets[aid] = a
			}
		}
		out = model.Scene{ID: id, Name: name, Revision: 1, State: "active", Document: p.Snapshot.Document, UpdatedAt: time.Now().UTC()}
		st.Scenes[id] = out
		st.Emit("scene.imported", id)
		return nil
	})
	if e != nil {
		if model.ReportOf(e) == nil {
			e = persist.Diagnose(e)
		}
	}
	return out, e
}

// bindImport retargets document diagnostics to the destination scene while
// keeping the content digest and codes unchanged. Package, asset and
// snapshot-scoped diagnostics are already bound to the content they inspected.
func bindImport(err error, id string, p bundle.Package) error {
	report := model.ReportOf(err)
	if report == nil || report.Subject.Kind != model.SubjectDocument {
		return err
	}
	return model.RebindSubject(err, model.DocumentSubject("import", id, p.Snapshot.Document))
}
