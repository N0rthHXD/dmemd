package retrieval

import "github.com/N0rthHXD/dmemd/internal/artifact"

// ArtifactDocumentBuilder is the production derived-document builder. It uses
// the same normalization and dictionary segmentation as atomic recall so a
// derived query and its index cannot disagree about token boundaries.
type ArtifactDocumentBuilder struct{}

// NewArtifactDocumentBuilder returns the production builder for runtime
// assembly.
func NewArtifactDocumentBuilder() ArtifactDocumentBuilder { return ArtifactDocumentBuilder{} }

// BuildArtifactDocument implements artifact.DocumentBuilder.
func (ArtifactDocumentBuilder) BuildArtifactDocument(input artifact.DocumentInput) (artifact.SearchDocument, error) {
	return artifact.SearchDocument{
		ArtifactID:   input.ArtifactID,
		Version:      input.Version,
		TitleTerms:   IndexText(input.Title),
		ContentTerms: IndexText(input.Content),
	}, nil
}

var _ artifact.DocumentBuilder = ArtifactDocumentBuilder{}
