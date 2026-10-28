package resources

// Resources are registered in dependency order: later registrations may attach
// sublists to earlier ones.
func init() {
	registerUsers()
	registerOrganizations()
	registerFolders()
	registerCredentials()
	registerInfrastructure()
	registerInventories()
	registerHostMetrics()
	registerUnified()
	registerProjects()
	registerInventorySources()
	registerAdHocCommands()
	registerJobTemplates()
	registerSystemJobs()
	registerWorkflows()
	registerSchedules()
	registerNotifications()
	registerWebhooks()
	registerRoles()
	registerActivity()
	registerRelatedLists()
	registerNamedURLs()
}
