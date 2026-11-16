package codec

import (
	"atlas-render/internal/model"
	"bytes"
	"image"
	"image/png"
)

// Diagnostic codes for asset bytes. They are content-scoped: the checked
// content is the uploaded byte stream, identified by its digest.
const (
	CodeAssetSize       = "asset.size"
	CodeAssetPNG        = "asset.png"
	CodeAssetDimensions = "asset.dimensions"
	CodeOutput          = "artifact.dimensions"
)

// Decode validates an uploaded PNG and returns the decoded image. Rejections
// carry diagnostics scoped to the uploaded bytes.
func Decode(data []byte) (image.Image, error) {
	if len(data) == 0 || len(data) > 2<<20 {
		return nil, model.AttachSubject(model.Diagnosed("PNG size must be 1..2MiB", model.Diagnostic{
			Code: CodeAssetSize, Severity: "error",
			Message: "asset PNG must be 1 byte..2MiB", Field: "asset",
			Evidence: map[string]any{"bytes": len(data), "limit": 2 << 20},
		}), byteSubject(data))
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(data))
	if e != nil {
		return nil, model.AttachSubject(model.Diagnosed("invalid PNG: "+e.Error(), model.Diagnostic{
			Code: CodeAssetPNG, Severity: "error",
			Message: "asset is not a decodable PNG: " + e.Error(), Field: "asset",
			Evidence: map[string]any{"bytes": len(data)},
		}), byteSubject(data))
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 1024 || cfg.Height > 1024 {
		return nil, model.AttachSubject(model.Diagnosed("asset dimensions must be 1..1024", model.Diagnostic{
			Code: CodeAssetDimensions, Severity: "error",
			Message:  "asset dimensions must each be 1..1024",
			Field:    "asset",
			Evidence: map[string]any{"width": cfg.Width, "height": cfg.Height, "limit": 1024},
		}), byteSubject(data))
	}
	img, e := png.Decode(bytes.NewReader(data))
	if e != nil {
		return nil, model.AttachSubject(model.Diagnosed("PNG decoding failed: "+e.Error(), model.Diagnostic{
			Code: CodeAssetPNG, Severity: "error",
			Message: "asset PNG could not be decoded: " + e.Error(), Field: "asset",
			Evidence: map[string]any{"bytes": len(data)},
		}), byteSubject(data))
	}
	return img, nil
}

func byteSubject(data []byte) model.DiagnosticSubject {
	return model.DiagnosticSubject{Kind: model.SubjectAsset, Origin: "import", ContentDigest: model.Digest(data)}
}

func Encode(img image.Image) ([]byte, error) {
	var b bytes.Buffer
	e := png.Encode(&b, img)
	return b.Bytes(), e
}

func DecodeOutput(data []byte) (image.Image, error) {
	if len(data) > 40<<20 {
		return nil, model.Invalid("output PNG exceeds limit")
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 3072 || cfg.Height > 3072 {
		return nil, model.Invalid("invalid output dimensions")
	}
	return png.Decode(bytes.NewReader(data))
}
