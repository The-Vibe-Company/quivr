package content

// IngestionEvaluation is independent work on one Version, projection and
// registration. Its failures are diagnostics of that plugin, not Version state.
type IngestionEvaluation struct {
	WorkQueue                                           string
	Organization, ID, RecordID, VersionID, GenerationID string
	PluginID, RegistrationID, PlanID                    string
	Spaces                                              []string
	State                                               string
}
