package model

import "errors"

// DiagnosticProtocolVersion is the version of the opt-in structured error
// protocol. The legacy {"code","error"} envelope remains the default.
const DiagnosticProtocolVersion = 2

// DiagnosticsMediaType selects the structured diagnostic protocol through the
// Accept header. Clients may also send "Atlas-Diagnostics: v2".
const DiagnosticsMediaType = "application/vnd.atlas.diagnostics.v2+json"

// Document subject kinds. Every diagnostic names the kind of content it was
// computed from plus that content's canonical digest.
const (
	SubjectDocument = "document"
	SubjectSnapshot = "snapshot"
	SubjectPackage  = "package"
	SubjectAsset    = "asset"
)

// LayerRef identifies an enclosing layer in the submitted document tree.
type LayerRef struct {
	Path string `json:"path"`
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"`
}

// LayerLocator points back to the exact layer relationship inside the content
// that was checked. Path is a JSON Pointer into the document, for example
// "/layers/2/0". Ancestors lists the enclosing groups from outermost to
// innermost.
type LayerLocator struct {
	Path      string     `json:"path"`
	ID        string     `json:"id"`
	Kind      string     `json:"kind,omitempty"`
	Ancestors []LayerRef `json:"ancestors,omitempty"`
}

// WorkContribution records how many layer-pixels one drawable layer adds to
// the compiled render cost. It explains why individually valid layers can
// exceed the combined budget.
type WorkContribution struct {
	Layer  LayerLocator `json:"layer"`
	Pixels int64        `json:"pixels"`
}

// Diagnostic is one machine-readable reason tied to the checked content.
type Diagnostic struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Message  string         `json:"message"`
	Layer    *LayerLocator  `json:"layer,omitempty"`
	Field    string         `json:"field,omitempty"`
	Related  []LayerLocator `json:"related,omitempty"`
	Evidence map[string]any `json:"evidence,omitempty"`
}

// DiagnosticSubject binds a diagnostic report to the exact content that was
// checked. ContentDigest is always the canonical digest of the submitted
// document; snapshot-backed checks additionally pin the snapshot digest and
// scale, so a scene edited afterwards cannot be mistaken for the checked
// content. Origin names the entry point that performed the check.
type DiagnosticSubject struct {
	Kind           string `json:"kind"`
	Origin         string `json:"origin"`
	ContentDigest  string `json:"contentDigest"`
	SnapshotDigest string `json:"snapshotDigest,omitempty"`
	SnapshotID     string `json:"snapshotId,omitempty"`
	SceneID        string `json:"sceneId,omitempty"`
	Revision       int    `json:"revision,omitempty"`
	Scale          int    `json:"scale,omitempty"`
}

// DiagnosticReport is the v2 error payload.
type DiagnosticReport struct {
	ProtocolVersion int               `json:"protocolVersion"`
	Subject         DiagnosticSubject `json:"subject"`
	Diagnostics     []Diagnostic      `json:"diagnostics"`
}

// DocumentDigest is the stable content identity used by diagnostics.
func DocumentDigest(d Document) string { return CanonicalDigest(d) }

// DocumentSubject binds diagnostics to a submitted document. Saving a scene
// and importing a package that carries the same document produce the same
// content digest and therefore the same basis for the diagnostics.
func DocumentSubject(origin, sceneID string, d Document) DiagnosticSubject {
	return DiagnosticSubject{Kind: SubjectDocument, Origin: origin, ContentDigest: DocumentDigest(d), SceneID: sceneID}
}

// SnapshotSubject binds diagnostics to the frozen snapshot content that was
// actually compiled, independently of any later scene revision.
func SnapshotSubject(origin string, s Snapshot, scale int) DiagnosticSubject {
	return DiagnosticSubject{
		Kind:           SubjectSnapshot,
		Origin:         origin,
		ContentDigest:  DocumentDigest(s.Document),
		SnapshotDigest: s.Digest,
		SnapshotID:     s.ID,
		SceneID:        s.SceneID,
		Revision:       s.Revision,
		Scale:          scale,
	}
}

// AttachSubject associates diagnostics with the content that produced them.
// Errors without diagnostics (for example malformed JSON or a missing entity)
// pass through unchanged, because there is no trustworthy checked content to
// bind them to. An already-bound subject is never overwritten, so a nested
// check keeps naming the content it actually inspected.
func AttachSubject(err error, subject DiagnosticSubject) error {
	var de *Error
	if !errors.As(err, &de) || len(de.Diagnostics) == 0 {
		return err
	}
	if de.Subject.ContentDigest != "" || de.Subject.Kind != "" {
		return err
	}
	copy := *de
	copy.Subject = subject
	return &copy
}

// RebindSubject replaces an existing subject. Callers use it only when they
// know the diagnostics were computed from the newly named content.
func RebindSubject(err error, subject DiagnosticSubject) error {
	var de *Error
	if !errors.As(err, &de) || len(de.Diagnostics) == 0 {
		return err
	}
	copy := *de
	copy.Subject = subject
	return &copy
}

// Report builds the v2 payload for an error, or nil when the error carries no
// structured diagnostic.
func (e *Error) Report() *DiagnosticReport {
	if len(e.Diagnostics) == 0 {
		return nil
	}
	return &DiagnosticReport{
		ProtocolVersion: DiagnosticProtocolVersion,
		Subject:         e.Subject,
		Diagnostics:     e.Diagnostics,
	}
}

// ReportOf extracts a structured report from a domain error, if present.
func ReportOf(err error) *DiagnosticReport {
	var de *Error
	if !errors.As(err, &de) {
		return nil
	}
	return de.Report()
}
