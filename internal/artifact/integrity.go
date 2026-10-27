package artifact

// Provenance is stored with the immutable revision, not reconstructed from
// mutable tables. MemoryRefs and RunRefs on Revision are the actual DB edges.
type Provenance struct {
	MemoryRefs       []MemoryRef `json:"memory_refs"`
	RunRefs          []RunRef    `json:"run_refs"`
	RunID            string      `json:"run_id"`
	TaxonomyVersion  string      `json:"taxonomy_version"`
	GeneratorVersion string      `json:"generator_version"`
	ProtocolVersion  string      `json:"protocol_version"`
	Truncated        bool        `json:"truncated"`
}

type IntegrityIssue struct {
	ArtifactID string `json:"artifact_id"`
	Version    int    `json:"version"`
	Code       string `json:"code"`
}

type IntegrityReport struct {
	Checked int              `json:"checked"`
	Issues  []IntegrityIssue `json:"issues"`
}
