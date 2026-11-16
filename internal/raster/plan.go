package raster

import (
	"atlas-render/internal/codec"
	"atlas-render/internal/geometry"
	"atlas-render/internal/model"
	"atlas-render/internal/scene"
	"image"
	"math"
	"sort"
	"strconv"
)

// Compile-time diagnostic codes for problems that only exist for a particular
// combination of layers, even though each layer is valid on its own.
const (
	CodeSingularTransform = "render.singularTransform"
	CodeMissingAsset      = "render.missingAsset"
	CodeBudget            = "render.workBudget"
	CodeScale             = "render.scale"
)

// WorkBudget is the maximum compiled layer-pixels accepted per job.
const WorkBudget = 40_000_000

type clip struct {
	inverse geometry.Matrix
	rect    model.Rect
}
type operation struct {
	layer   model.Layer
	inverse geometry.Matrix
	clips   []clip
	opacity float64
	bounds  image.Rectangle
	bitmap  image.Image
}
type Plan struct {
	Width, Height, Scale int
	Background           string
	ops                  []operation
	Cost                 int64
}

// Compile validates and compiles a frozen snapshot. It never queues work: all
// diagnostics are produced from the exact snapshot content passed in, and any
// rejection happens before the job is accepted.
//
// When the budget is exceeded, Compile reports how much each drawable layer
// contributed so a client can tell which combination of individually valid
// layers crossed the limit.
func Compile(s model.Snapshot, scale int) (Plan, error) {
	if e := scene.Validate(s.Document); e != nil {
		return Plan{}, e
	}
	if scale != 1 && scale != 2 {
		return Plan{}, model.Diagnosed("scale must be one or two", model.Diagnostic{
			Code: CodeScale, Severity: "error", Message: "output scale must be 1 or 2",
			Field: "scale", Evidence: map[string]any{"scale": scale},
		})
	}
	p := Plan{Width: s.Document.Width * scale, Height: s.Document.Height * scale, Scale: scale, Background: s.Document.Background}
	contributions := []model.WorkContribution{}
	var walk func([]model.Layer, string, []model.LayerRef, geometry.Matrix, []clip, float64) error
	walk = func(ls []model.Layer, base string, parents []model.LayerRef, parent geometry.Matrix, clips []clip, alpha float64) error {
		for i := range ls {
			l := ls[i]
			path := base + "/" + strconv.Itoa(i)
			world := parent.Mul(geometry.From(l.Transform))
			inv, ok := world.Inverse()
			if !ok {
				loc := model.LayerLocator{Path: path, ID: l.ID, Kind: l.Kind, Ancestors: cloneRefs(parents)}
				return model.Diagnosed("layer "+l.ID+" has singular accumulated transform", model.Diagnostic{
					Code: CodeSingularTransform, Severity: "error",
					Message: "layer " + l.ID + " has a singular accumulated transform; the combined layer scales collapse the local space",
					Layer:   &loc, Field: "transform",
					Evidence: map[string]any{
						"determinant": world.A*world.D - world.B*world.C,
						"scaleX":      l.Transform.ScaleX, "scaleY": l.Transform.ScaleY,
						"ancestors": len(parents),
					},
				})
			}
			localClips := append([]clip(nil), clips...)
			if l.Clip != nil {
				localClips = append(localClips, clip{inverse: inv, rect: *l.Clip})
			}
			a := alpha * l.Opacity
			if l.Kind == "group" {
				refs := append(cloneRefs(parents), model.LayerRef{Path: path, ID: l.ID, Kind: l.Kind})
				if e := walk(l.Children, path, refs, world, localClips, a); e != nil {
					return e
				}
				continue
			}
			corners := []model.Point{{}, {X: l.Width}, {Y: l.Height}, {X: l.Width, Y: l.Height}}
			minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for _, c := range corners {
				v := world.Apply(c)
				minX = math.Min(minX, v.X)
				minY = math.Min(minY, v.Y)
				maxX = math.Max(maxX, v.X)
				maxY = math.Max(maxY, v.Y)
			}
			minX = math.Max(0, math.Min(float64(s.Document.Width), minX))
			maxX = math.Max(0, math.Min(float64(s.Document.Width), maxX))
			minY = math.Max(0, math.Min(float64(s.Document.Height), minY))
			maxY = math.Max(0, math.Min(float64(s.Document.Height), maxY))
			b := image.Rect(int(math.Floor(minX*float64(scale))), int(math.Floor(minY*float64(scale))), int(math.Ceil(maxX*float64(scale))), int(math.Ceil(maxY*float64(scale))))
			op := operation{layer: l, inverse: inv, clips: localClips, opacity: a, bounds: b}
			if l.Kind == "image" {
				if _, ok := s.Assets[l.AssetID]; !ok {
					loc := model.LayerLocator{Path: path, ID: l.ID, Kind: l.Kind, Ancestors: cloneRefs(parents)}
					return model.Diagnosed("missing pinned asset "+l.AssetID, model.Diagnostic{
						Code: CodeMissingAsset, Severity: "error",
						Message: "image layer " + l.ID + " references asset " + l.AssetID + ", which is not pinned in this snapshot",
						Layer:   &loc, Field: "assetId", Evidence: map[string]any{"assetId": l.AssetID},
					})
				}
			}
			pixels := int64(b.Dx()) * int64(b.Dy())
			p.Cost += pixels
			contributions = append(contributions, model.WorkContribution{
				Layer:  model.LayerLocator{Path: path, ID: l.ID, Kind: l.Kind, Ancestors: cloneRefs(parents)},
				Pixels: pixels,
			})
			p.ops = append(p.ops, op)
		}
		return nil
	}
	if e := walk(s.Document.Layers, "/layers", nil, geometry.Identity(), nil, 1); e != nil {
		return Plan{}, e
	}
	// The budget is reported only after the whole tree compiles, so every
	// layer's contribution is known and nothing has been accepted yet.
	if p.Cost > WorkBudget {
		return Plan{}, budgetError(p.Cost, contributions)
	}
	// Decode bitmaps only after the budget is accepted, so a rejected job does
	// no expensive image work and its diagnostics stay about the geometry.
	decoded := map[string]image.Image{}
	for i := range p.ops {
		op := &p.ops[i]
		if op.layer.Kind != "image" {
			continue
		}
		img := decoded[op.layer.AssetID]
		if img == nil {
			var e error
			img, e = codec.Decode(s.Assets[op.layer.AssetID].PNG)
			if e != nil {
				return Plan{}, e
			}
			decoded[op.layer.AssetID] = img
		}
		op.bitmap = img
	}
	return p, nil
}

func budgetError(cost int64, contributions []model.WorkContribution) error {
	sorted := append([]model.WorkContribution(nil), contributions...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Pixels > sorted[j].Pixels })
	top := sorted
	if len(top) > 8 {
		top = top[:8]
	}
	related := make([]model.LayerLocator, 0, len(top))
	for _, c := range top {
		related = append(related, c.Layer)
	}
	main := related[0]
	return model.DiagnosedStatus(429, "capacity", "scene exceeds raster work budget", model.Diagnostic{
		Code: CodeBudget, Severity: "error",
		Message: "the combined drawable layers need " + strconv.FormatInt(cost, 10) + " layer-pixels, above the " + strconv.FormatInt(WorkBudget, 10) + " budget",
		Layer:   &main, Field: "layers", Related: related,
		Evidence: map[string]any{
			// cost is the sum over every drawable layer, including the ones not
			// listed here. contributions shows at most the eight largest so the
			// payload stays bounded.
			"cost": cost, "budget": WorkBudget,
			"drawableLayers": len(contributions), "shown": len(top),
			"contributions": top,
		},
	})
}

func cloneRefs(refs []model.LayerRef) []model.LayerRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]model.LayerRef, len(refs))
	copy(out, refs)
	return out
}
