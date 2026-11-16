package scene

import (
	"atlas-render/internal/model"
	"atlas-render/internal/paint"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

// Diagnostic codes emitted by document validation. They are stable so editors
// can map a rejection back to the field that needs adjustment.
const (
	CodeCanvas           = "scene.canvas"
	CodeBackground       = "scene.background"
	CodeNesting          = "scene.nesting"
	CodeLayerCount       = "scene.layerCount"
	CodeLayerID          = "layer.id"
	CodeLayerGeometry    = "layer.geometry"
	CodeLayerOpacity     = "layer.opacityScale"
	CodeLayerClip        = "layer.clip"
	CodeGroupEmpty       = "group.empty"
	CodeChildren         = "layer.children"
	CodeLayerDimensions  = "layer.dimensions"
	CodeLayerKind        = "layer.kind"
	CodePolygonPoints    = "polygon.points"
	CodePolygonBounds    = "polygon.bounds"
	CodeImageAsset       = "image.assetId"
	CodePaintKind        = "paint.kind"
	CodePaintColor       = "paint.color"
	CodeGradientColor    = "paint.gradientColor"
	CodeGradientEndpoint = "paint.gradientEndpoint"
	CodeGradientCoincide = "paint.gradientCoincident"
)

func ValidID(id string) bool { return identifier.MatchString(id) }
func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 100000 }

type walker struct {
	seen   map[string]string
	count  int
	ancest []model.LayerRef
}

func (w *walker) locator(l model.Layer, path string) model.LayerLocator {
	ancestors := make([]model.LayerRef, len(w.ancest))
	copy(ancestors, w.ancest)
	return model.LayerLocator{Path: path, ID: l.ID, Kind: l.Kind, Ancestors: ancestors}
}

// LocationsForAsset returns every image layer that references assetID, with its
// document path and enclosing groups. It lets reference problems such as a
// retired asset point back at the submitted layer relationships.
func LocationsForAsset(d model.Document, assetID string) []model.LayerLocator {
	out := []model.LayerLocator{}
	var walk func([]model.Layer, string, []model.LayerRef)
	walk = func(layers []model.Layer, base string, parents []model.LayerRef) {
		for i := range layers {
			l := layers[i]
			path := base + "/" + strconv.Itoa(i)
			if l.Kind == "image" && l.AssetID == assetID {
				ancestors := make([]model.LayerRef, len(parents))
				copy(ancestors, parents)
				out = append(out, model.LayerLocator{Path: path, ID: l.ID, Kind: l.Kind, Ancestors: ancestors})
			}
			if len(l.Children) > 0 {
				refs := append(append([]model.LayerRef(nil), parents...), model.LayerRef{Path: path, ID: l.ID, Kind: l.Kind})
				walk(l.Children, path, refs)
			}
		}
	}
	walk(d.Layers, "/layers", nil)
	return out
}

// Validate checks a document and returns a *model.Error carrying diagnostics
// that point at the offending layer path. The message text is unchanged from
// the pre-diagnostic baseline so legacy clients see the same "error" string.
func Validate(d model.Document) error {
	w := &walker{seen: map[string]string{}}
	if d.Width < 1 || d.Height < 1 || d.Width > 1536 || d.Height > 1536 {
		return model.Diagnosed("canvas dimensions must be 1..1536", model.Diagnostic{
			Code: CodeCanvas, Severity: "error", Message: "canvas width and height must each be 1..1536",
			Field: "width,height", Evidence: map[string]any{"width": d.Width, "height": d.Height},
		})
	}
	if _, e := paint.Parse(d.Background); e != nil {
		return model.Diagnosed("background: "+e.Error(), model.Diagnostic{
			Code: CodeBackground, Severity: "error", Message: "background color must be #RRGGBB",
			Field: "background", Evidence: map[string]any{"background": d.Background},
		})
	}
	return w.walk(d.Layers, 0, "/layers")
}

func (w *walker) walk(layers []model.Layer, depth int, base string) error {
	if depth > 8 {
		loc := model.LayerLocator{Path: base}
		if n := len(w.ancest); n > 0 {
			a := w.ancest[n-1]
			loc.ID, loc.Kind = a.ID, a.Kind
		}
		return model.Diagnosed("scene nesting exceeds eight", model.Diagnostic{
			Code: CodeNesting, Severity: "error", Message: "group nesting may not exceed eight levels",
			Layer: &loc, Field: "children", Evidence: map[string]any{"depth": depth, "limit": 8},
		})
	}
	for i := range layers {
		l := layers[i]
		path := base + "/" + strconv.Itoa(i)
		w.count++
		if w.count > 200 {
			loc := w.locator(l, path)
			return model.Diagnosed("scene exceeds 200 layers", model.Diagnostic{
				Code: CodeLayerCount, Severity: "error", Message: "document may contain at most 200 layers",
				Layer: &loc, Field: "layers", Evidence: map[string]any{"count": w.count, "limit": 200},
			})
		}
		if !ValidID(l.ID) || w.seen[l.ID] != "" {
			loc := w.locator(l, path)
			reason := "must match the layer identifier pattern"
			if w.seen[l.ID] != "" {
				reason = "repeats an earlier layer identifier"
			}
			diag := model.Diagnostic{
				Code: CodeLayerID, Severity: "error", Message: "layer id invalid or repeated: " + l.ID,
				Layer: &loc, Field: "id", Evidence: map[string]any{"id": l.ID, "reason": reason},
			}
			if first := w.seen[l.ID]; first != "" && first != path {
				diag.Related = append(diag.Related, model.LayerLocator{Path: first, ID: l.ID, Kind: l.Kind})
			}
			return model.Diagnosed("layer id invalid or repeated: "+l.ID, diag)
		}
		w.seen[l.ID] = path
		t := l.Transform
		numbers := []struct {
			name  string
			value float64
		}{
			{"width", l.Width}, {"height", l.Height}, {"opacity", l.Opacity},
			{"transform.x", t.X}, {"transform.y", t.Y},
			{"transform.scaleX", t.ScaleX}, {"transform.scaleY", t.ScaleY},
			{"transform.degrees", t.Degrees},
		}
		for _, n := range numbers {
			if !finite(n.value) {
				loc := w.locator(l, path)
				return model.Diagnosed("layer "+l.ID+": nonfinite or unbounded geometry", model.Diagnostic{
					Code: CodeLayerGeometry, Severity: "error",
					Message: "layer " + l.ID + ": " + n.name + " must be finite and within magnitude 100000",
					Layer:   &loc, Field: n.name,
					Evidence: map[string]any{"value": fmt.Sprint(n.value), "limit": 100000},
				})
			}
		}
		if l.Opacity < 0 || l.Opacity > 1 || math.Abs(t.ScaleX) < .001 || math.Abs(t.ScaleY) < .001 || math.Abs(t.ScaleX) > 100 || math.Abs(t.ScaleY) > 100 {
			loc := w.locator(l, path)
			field := "opacity"
			msg := "layer " + l.ID + ": opacity must be 0..1"
			evidence := map[string]any{"opacity": l.Opacity}
			if l.Opacity >= 0 && l.Opacity <= 1 {
				field = "transform.scaleX"
				msg = "layer " + l.ID + ": scale magnitude must be 0.001..100"
				evidence = map[string]any{"scaleX": t.ScaleX, "scaleY": t.ScaleY}
			}
			return model.Diagnosed("layer "+l.ID+": invalid opacity or scale", model.Diagnostic{
				Code: CodeLayerOpacity, Severity: "error", Message: msg,
				Layer: &loc, Field: field, Evidence: evidence,
			})
		}
		if l.Clip != nil {
			r := l.Clip
			bad := ""
			switch {
			case !finite(r.X):
				bad = "x"
			case !finite(r.Y):
				bad = "y"
			case !finite(r.Width) || r.Width <= 0:
				bad = "width"
			case !finite(r.Height) || r.Height <= 0:
				bad = "height"
			}
			if bad != "" {
				loc := w.locator(l, path)
				return model.Diagnosed("layer "+l.ID+": invalid clip", model.Diagnostic{
					Code: CodeLayerClip, Severity: "error",
					Message: "layer " + l.ID + ": clip." + bad + " must be finite" + positiveNote(bad),
					Layer:   &loc, Field: "clip." + bad,
					Evidence: map[string]any{"x": r.X, "y": r.Y, "width": r.Width, "height": r.Height},
				})
			}
		}
		if l.Kind == "group" {
			if len(l.Children) == 0 {
				loc := w.locator(l, path)
				return model.Diagnosed("group "+l.ID+" is empty", model.Diagnostic{
					Code: CodeGroupEmpty, Severity: "error", Message: "group " + l.ID + " must contain at least one child",
					Layer: &loc, Field: "children",
				})
			}
			w.ancest = append(w.ancest, model.LayerRef{Path: path, ID: l.ID, Kind: l.Kind})
			e := w.walk(l.Children, depth+1, path)
			w.ancest = w.ancest[:len(w.ancest)-1]
			if e != nil {
				return e
			}
			continue
		}
		if len(l.Children) > 0 {
			loc := w.locator(l, path)
			return model.Diagnosed("only groups can contain layers", model.Diagnostic{
				Code: CodeChildren, Severity: "error",
				Message: "layer " + l.ID + " of kind " + l.Kind + " cannot contain children; use a group",
				Layer:   &loc, Field: "children", Evidence: map[string]any{"kind": l.Kind},
			})
		}
		if l.Width <= 0 || l.Height <= 0 || l.Width > 4096 || l.Height > 4096 {
			loc := w.locator(l, path)
			field := "width"
			if l.Width > 0 && l.Width <= 4096 {
				field = "height"
			}
			return model.Diagnosed("layer "+l.ID+": invalid dimensions", model.Diagnostic{
				Code: CodeLayerDimensions, Severity: "error",
				Message: "layer " + l.ID + ": width and height must be 1..4096",
				Layer:   &loc, Field: field, Evidence: map[string]any{"width": l.Width, "height": l.Height},
			})
		}
		switch l.Kind {
		case "rectangle", "ellipse":
		case "polygon":
			if len(l.Points) < 3 || len(l.Points) > 128 {
				loc := w.locator(l, path)
				return model.Diagnosed("polygon requires 3..128 points", model.Diagnostic{
					Code: CodePolygonPoints, Severity: "error", Message: "polygon requires 3..128 points",
					Layer: &loc, Field: "points", Evidence: map[string]any{"points": len(l.Points)},
				})
			}
			for pi, p := range l.Points {
				if !finite(p.X) || !finite(p.Y) || p.X < 0 || p.X > l.Width || p.Y < 0 || p.Y > l.Height {
					loc := w.locator(l, path)
					return model.Diagnosed("polygon point outside local bounds", model.Diagnostic{
						Code: CodePolygonBounds, Severity: "error",
						Message: "polygon point must lie inside the layer local bounds",
						Layer:   &loc, Field: "points." + strconv.Itoa(pi),
						Evidence: map[string]any{"x": p.X, "y": p.Y, "width": l.Width, "height": l.Height},
					})
				}
			}
		case "image":
			if !ValidID(l.AssetID) {
				loc := w.locator(l, path)
				return model.Diagnosed("image requires asset id", model.Diagnostic{
					Code: CodeImageAsset, Severity: "error", Message: "image layer requires a valid assetId",
					Layer: &loc, Field: "assetId", Evidence: map[string]any{"assetId": l.AssetID},
				})
			}
			continue
		default:
			loc := w.locator(l, path)
			return model.Diagnosed("layer "+l.ID+": unknown kind", model.Diagnostic{
				Code: CodeLayerKind, Severity: "error",
				Message: "layer " + l.ID + ": unknown kind; expected rectangle, ellipse, polygon, image or group",
				Layer:   &loc, Field: "kind", Evidence: map[string]any{"kind": l.Kind},
			})
		}
		if l.Fill.Kind != "solid" && l.Fill.Kind != "linear" {
			loc := w.locator(l, path)
			return model.Diagnosed("unknown paint kind", model.Diagnostic{
				Code: CodePaintKind, Severity: "error",
				Message: "paint kind must be solid or linear",
				Layer:   &loc, Field: "fill.kind", Evidence: map[string]any{"kind": l.Fill.Kind},
			})
		}
		if _, e := paint.Parse(l.Fill.Color); e != nil {
			loc := w.locator(l, path)
			return model.Diagnosed("invalid fill color", model.Diagnostic{
				Code: CodePaintColor, Severity: "error",
				Message: "fill.color must be #RRGGBB",
				Layer:   &loc, Field: "fill.color", Evidence: map[string]any{"color": l.Fill.Color},
			})
		}
		if l.Fill.Kind == "linear" {
			if _, e := paint.Parse(l.Fill.EndColor); e != nil {
				loc := w.locator(l, path)
				return model.Diagnosed("invalid gradient color", model.Diagnostic{
					Code: CodeGradientColor, Severity: "error",
					Message: "fill.endColor must be #RRGGBB",
					Layer:   &loc, Field: "fill.endColor", Evidence: map[string]any{"endColor": l.Fill.EndColor},
				})
			}
			bad := ""
			for _, n := range []struct {
				name  string
				value float64
			}{
				{"fill.from.x", l.Fill.From.X}, {"fill.from.y", l.Fill.From.Y},
				{"fill.to.x", l.Fill.To.X}, {"fill.to.y", l.Fill.To.Y},
			} {
				if !finite(n.value) {
					bad = n.name
					break
				}
			}
			if bad != "" {
				loc := w.locator(l, path)
				return model.Diagnosed("invalid gradient endpoint", model.Diagnostic{
					Code: CodeGradientEndpoint, Severity: "error",
					Message: "gradient endpoints must be finite",
					Layer:   &loc, Field: bad,
					Evidence: map[string]any{"from": l.Fill.From, "to": l.Fill.To},
				})
			}
			if l.Fill.From == l.Fill.To {
				loc := w.locator(l, path)
				return model.Diagnosed("gradient endpoints coincide", model.Diagnostic{
					Code: CodeGradientCoincide, Severity: "error",
					Message: "gradient from and to must differ so the gradient direction is defined",
					Layer:   &loc, Field: "fill.from,fill.to",
					Evidence: map[string]any{"from": l.Fill.From, "to": l.Fill.To},
				})
			}
		}
	}
	return nil
}

func positiveNote(field string) string {
	if field == "width" || field == "height" {
		return " and positive"
	}
	return ""
}

func References(d model.Document) []string {
	seen := map[string]bool{}
	out := []string{}
	var walk func([]model.Layer)
	walk = func(ls []model.Layer) {
		for _, l := range ls {
			if l.Kind == "image" && !seen[l.AssetID] {
				seen[l.AssetID] = true
				out = append(out, l.AssetID)
			}
			walk(l.Children)
		}
	}
	walk(d.Layers)
	return out
}
